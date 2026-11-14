# Quick start

This complete program supervises `go version` once. Replace the `Spec` factory
with your binary and select `RestartAlways` to restart after every exit.

```go
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
 if err != nil { panic(err) }
 outcome := supervisor.Run(context.Background())
 if outcome.Last != nil { fmt.Println("exit:", outcome.Last.Code) }
}

```

Run the checked-in example with `go run ./examples/supervise`.

For a long-running application, pass its shutdown context to `Run`. Cancelling
that context requests graceful termination and waits for cleanup before returning.
`Stop` requests an intentional stop; follow it by waiting for `Run` to return.
`Stop` alone is non-blocking. Use a separate `Supervisor` and goroutine for each
independent binary and wait for all those goroutines during application shutdown.

The factory runs for every attempt, including spawn retries. It may choose a new
path, environment, directory, or fresh pipes. Observe start failures via events;
a failing factory is retried with backoff even under `RestartNever`, which governs
exits of successfully started processes.
