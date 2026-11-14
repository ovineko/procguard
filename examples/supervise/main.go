// Command supervise demonstrates a complete run that ends after one child exit.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ovineko/procguard"
)

func main() {
	supervisor, err := procguard.NewSupervisor(procguard.Options{
		Spec: func(context.Context) (procguard.Spec, error) {
			return procguard.Spec{Path: "go", Args: []string{"version"}, Stdout: os.Stdout}, nil
		},
		Restart: procguard.RestartPolicy{Mode: procguard.RestartNever},
	})
	if err != nil {
		panic(err)
	}
	outcome := supervisor.Run(context.Background())
	if outcome.Last != nil {
		fmt.Println("exit:", outcome.Last.Code)
	}
}
