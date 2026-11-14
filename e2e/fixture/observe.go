//go:build e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"

	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

func observedPIDs(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read readiness: %w", err)
	}
	var pids []int
	for _, entry := range entries {
		if entry.Name() == "supervisor.ready" || !strings.HasSuffix(entry.Name(), ".ready") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ".ready"))
		if err != nil {
			return nil, fmt.Errorf("parse ready PID: %w", err)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func observe(c config) error {
	if err := escapeSession(); err != nil {
		return err
	}
	var pids []int
	for raw := range strings.SplitSeq(c.pids, ",") {
		pid, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("observer PID: %w", err)
		}
		pids = append(pids, pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testsupport.Scale(750*time.Millisecond))
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var r wire.Record
	for {
		r = wire.Record{Event: "observed", PIDs: pids}
		for _, pid := range pids {
			alive, zombie, err := processState(pid)
			if err != nil {
				return err
			}
			r.Alive = append(r.Alive, alive)
			if zombie {
				r.Zombies++
			}
		}
		select {
		case <-ctx.Done():
			emit(r)
			return nil
		case <-ticker.C:
		}
	}
}

func observeExternal(pids []int) error {
	raw := make([]string, len(pids))
	for i, pid := range pids {
		raw[i] = strconv.Itoa(pid)
	}
	cmd := command("observer", "-pids", strings.Join(raw, ","))
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("external observer: %w", err)
	}
	return nil
}

func anchor(c config) error {
	if c.scenario == "watch-parent" {
		// Keep a separate intermediate ancestor even when this entrypoint is PID 1.
		// Adoption by namespace init alone would not prove subreaper behavior.
		cmd := command("crash-anchor", "-scenario", c.scenario, "-dir", c.dir, "-gate", c.gate)
		cmd.Stdout = os.Stdout
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("intermediate anchor: %w", err)
		}
		return nil
	}
	return crashAnchor(c)
}

func crashAnchor(c config) error {
	if err := subreaper(); err != nil {
		return err
	}
	if c.scenario == "hold" {
		terms, stop := termSignals("exit")
		defer stop()
		emit(wire.Record{Event: "ready", PID: os.Getpid()})
		<-terms
		return nil
	}
	cmd := command("supervise", "-scenario", c.scenario, "-dir", c.dir)
	var output bytes.Buffer
	cmd.Stdout = &output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start supervisor: %w", err)
	}
	defer func() { _ = cmd.Process.Kill(); reapOrphans() }()
	ctx, cancel := bounded(context.Background())
	defer cancel()
	if err := until(ctx, func() bool { return exists(filepath.Join(c.dir, "supervisor.ready")) }); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	pids, err := observedPIDs(c.dir)
	if err != nil {
		return err
	}
	defer func() {
		for _, pid := range pids {
			_ = hardKill(pid)
		}
		reapOrphans()
	}()
	if c.gate != "external" {
		if err := cmd.Process.Kill(); err != nil {
			return fmt.Errorf("kill supervisor: %w", err)
		}
	}
	_ = cmd.Wait()
	if _, err := os.Stdout.Write(output.Bytes()); err != nil {
		return fmt.Errorf("forward supervisor output: %w", err)
	}
	emit(wire.Record{Event: "supervisor-killed", PID: cmd.Process.Pid, ObservedPID: os.Getpid()})
	if c.scenario == "watch-parent" && runtime.GOOS != "windows" {
		if err := until(ctx, func() bool { return exists(filepath.Join(c.dir, "cause")) }); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(c.dir, "cause"))
		if err != nil {
			return fmt.Errorf("read cause: %w", err)
		}
		var record wire.Record
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("decode cause: %w", err)
		}
		emit(record)
	}
	if err := observeExternal(pids); err != nil {
		return err
	}
	return emitMarkers(c.dir, pids)
}

func emitZombieSample(dir string) {
	pids, err := observedPIDs(dir)
	must(err)
	zombies := 0
	for _, pid := range pids {
		_, zombie, err := processState(pid)
		must(err)
		if zombie {
			zombies++
		}
	}
	emit(wire.Record{Event: "zombie-sample", Zombies: zombies})
}

func emitMarkers(dir string, pids []int) error {
	r := wire.Record{Event: "markers"}
	for _, pid := range pids {
		// #nosec G304 -- Marker paths use fixture-owned directories and observed numeric PIDs.
		data, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)+".term"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read signal marker: %w", err)
		}
		if len(data) == 0 {
			return fmt.Errorf("empty signal marker for %d", pid)
		}
		r.PIDs = append(r.PIDs, pid)
	}
	r.Markers = len(r.PIDs)
	emit(r)
	return nil
}

func emitIdentities(dir string, pids []int) error {
	for _, pid := range pids {
		// #nosec G304 -- Identity paths use fixture-owned directories and observed numeric PIDs.
		data, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)+".ready"))
		if err != nil {
			return fmt.Errorf("read identity: %w", err)
		}
		var r wire.Record
		if err := json.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("decode identity: %w", err)
		}
		if r.PID != pid {
			return fmt.Errorf("identity PID mismatch: %d", pid)
		}
		emit(r)
	}
	return nil
}
