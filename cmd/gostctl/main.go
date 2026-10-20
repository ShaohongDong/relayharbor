package main

import (
	"context"
	"fmt"
	"gitee.com/kenny3755/gost_deploy/internal/cli"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if e := cli.Main(ctx, os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "错误："+e.Error())
		os.Exit(1)
	}
}
