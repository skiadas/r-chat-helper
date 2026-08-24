package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	cp "github.com/haris/r-chat-helper/control-plane"
)

// runAdmin executes admin subcommands against the same database the server
// uses (see env config in control-plane/config.go).
func runAdmin(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("admin requires a subcommand")
	}
	app, err := cp.New(cp.DefaultConfig())
	if err != nil {
		return err
	}
	defer app.Close()
	ctx := context.Background()

	switch args[0] {
	case "add-student":
		return cmdAddStudent(ctx, app, args[1:])
	case "set-active":
		return cmdSetActive(ctx, app, args[1:])
	case "set-budget":
		return cmdSetBudget(ctx, app, args[1:])
	case "list":
		return cmdList(ctx, app)
	case "sync-rates":
		n, err := app.SyncRates(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("rates synced (%d changed)\n", n)
		return nil
	default:
		return fmt.Errorf("unknown admin subcommand %q", args[0])
	}
}

func cmdAddStudent(ctx context.Context, app *cp.App, args []string) error {
	fs := flag.NewFlagSet("add-student", flag.ContinueOnError)
	email := fs.String("email", "", "student email (the SSO identity)")
	id := fs.String("id", "", "student id (defaults to the email)")
	name := fs.String("name", "", "display name")
	budget := fs.Float64("budget", 0, "soft budget in USD (required, > 0)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || *name == "" {
		return fmt.Errorf("add-student requires -email and -name")
	}
	if *id == "" {
		*id = *email
	}
	if *budget <= 0 {
		return fmt.Errorf("add-student requires -budget greater than zero")
	}
	if err := app.AddStudent(ctx, *id, *email, *name, int64(*budget*1e6)); err != nil {
		return err
	}
	fmt.Printf("added student %s (%s)\n", *id, *email)
	return nil
}

func cmdSetActive(ctx context.Context, app *cp.App, args []string) error {
	fs := flag.NewFlagSet("set-active", flag.ContinueOnError)
	email := fs.String("email", "", "student email")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return fmt.Errorf("set-active requires -email")
	}
	on := true
	rest := fs.Args()
	if len(rest) > 0 {
		on = rest[0] != "off"
	}
	if err := app.SetActive(ctx, *email, on); err != nil {
		return err
	}
	fmt.Printf("student %s active=%v\n", *email, on)
	return nil
}

func cmdSetBudget(ctx context.Context, app *cp.App, args []string) error {
	fs := flag.NewFlagSet("set-budget", flag.ContinueOnError)
	student := fs.String("student", "", "student id")
	budget := fs.Float64("budget", 0, "soft budget in USD (required, > 0)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *student == "" {
		return fmt.Errorf("set-budget requires -student and -budget")
	}
	if *budget <= 0 {
		return fmt.Errorf("set-budget requires -budget greater than zero")
	}
	s, err := app.StudentByID(ctx, *student)
	if err != nil || s == nil {
		return fmt.Errorf("unknown student %q", *student)
	}
	if err := app.SetBudget(ctx, s.ID, int64(*budget*1e6)); err != nil {
		return err
	}
	fmt.Printf("budget for %s set to %.4f USD\n", s.ID, *budget)
	return nil
}

func cmdList(ctx context.Context, app *cp.App) error {
	students, err := app.ListStudents(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tEMAIL\tNAME\tACTIVE\tSPENT (USD)\tBUDGET (USD)")
	for _, s := range students {
		spent, err := app.SpendByStudent(ctx, s.ID)
		if err != nil {
			return err
		}
		active := "no"
		if s.Active {
			active = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.4f\t%.4f\n", s.ID, s.Email, s.Name, active, float64(spent)/1e6, float64(s.BudgetMicros)/1e6)
	}
	w.Flush()
	return nil
}
