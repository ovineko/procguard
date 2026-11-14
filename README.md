# procguard

procguard provides cross-platform supervision and lifecycle management for child OS processes in Go.
It reports what happened to a process; the consumer decides what it means.

```sh
go get github.com/ovineko/procguard
```

A complete example (requires Go on PATH):

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

Read the [documentation](https://procguard.ovineko.com/), especially the
[platform guarantees](https://procguard.ovineko.com/guides/platform-guarantees/).

Licensed under the [MIT License](LICENSE).
