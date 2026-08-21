package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	cp "github.com/haris/r-chat-helper/control-plane"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load() // dev convenience; shell env wins, absence is fine
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		runServe()
	case "admin":
		if err := runAdmin(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func runServe() {
	app, err := cp.New(cp.DefaultConfig())
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `r-chat-helper — class R tutor control plane

usage:
  r-chat-helper serve           run the server
  r-chat-helper admin ...       admin commands (see below)

admin commands:
  add-student -email E -id ID -name NAME [-budget USD]
  set-active -email E [on|off]
  set-budget -student ID -budget USD
  list
  sync-rates
`)
}
