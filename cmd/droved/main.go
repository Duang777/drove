// Command droved 是 Drove 常驻 daemon 入口。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/drovehq/drove/internal/config"
	"github.com/drovehq/drove/internal/daemon"
)

func main() {
	cfgPath := ""
	if len(os.Args) > 1 && os.Args[1] == "--config" && len(os.Args) > 2 {
		cfgPath = os.Args[2]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droved:", err)
		os.Exit(1)
	}

	if err := daemon.New(cfg).Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "droved:", err)
		os.Exit(1)
	}
}
