package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	stdlog "log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianobject"
	"github.com/gin-gonic/gin"
	gormlogger "gorm.io/gorm/logger"
)

type commandOptions struct {
	command        string
	apply          bool
	reportPath     string
	expectDatabase string
	sourceTimeout  time.Duration
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mujian-migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	configureMigrationLogging()
	options, err := parseCommand(os.Args[1:])
	if err != nil {
		return err
	}
	dsn := strings.TrimSpace(os.Getenv("SQL_DSN"))
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return errors.New("SQL_DSN must be a PostgreSQL DSN")
	}

	// The application performs schema migrations. This command connects without
	// running unrelated startup jobs or silently creating application data.
	common.IsMasterNode = false
	if err = model.InitDB(); err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	sqlDB, err := model.DB.DB()
	if err != nil {
		return fmt.Errorf("open database handle: %w", err)
	}
	defer sqlDB.Close()
	currentDatabase, err := requireExpectedDatabase(options.expectDatabase)
	if err != nil {
		return err
	}
	if options.command == "schema" {
		if err = model.MigrateMainDB(); err != nil {
			return fmt.Errorf("migrate database schema: %w", err)
		}
	}
	if err = requireObjectColumns(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine := migrationEngine{db: model.DB, httpSource: newLegacyHTTPSource(options.sourceTimeout)}
	if options.command == "images-to-obs" || options.command == "verify" {
		store, enabled, storeErr := mujianobject.FromEnvironment()
		if storeErr != nil {
			return fmt.Errorf("open OBS store: %w", storeErr)
		}
		if !enabled {
			return errors.New("MUJIAN_IMAGE_STORAGE=obs is required for migration and verification")
		}
		engine.store = objectStoreAdapter{store: store}
	}

	var report migrationReport
	var commandErr error
	switch options.command {
	case "schema":
		report = newMigrationReport("schema", false)
		report.add(reportEntry{Kind: "database", ID: currentDatabase, Status: "migrated"})
		report.finish()
	case "images-to-obs":
		report, commandErr = engine.imagesToOBS(ctx, options.apply)
	case "verify":
		report, commandErr = engine.verify(ctx)
	case "report":
		report, commandErr = engine.inventory()
	default:
		return fmt.Errorf("unsupported command %q", options.command)
	}
	if reportErr := writeMigrationReport(options.reportPath, report); reportErr != nil {
		commandErr = errors.Join(commandErr, reportErr)
	}
	fmt.Fprintf(os.Stderr, "%s: total=%d report=%s\n", options.command, report.Summary["total"], options.reportPath)
	return commandErr
}

func parseCommand(args []string) (commandOptions, error) {
	if len(args) == 0 {
		return commandOptions{}, errors.New("usage: mujian-migrate schema|images-to-obs|verify|report [options]")
	}
	options := commandOptions{command: args[0], sourceTimeout: 3 * time.Minute}
	flags := flag.NewFlagSet(options.command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&options.reportPath, "report", "", "write JSON report to this path, or - for stdout")
	flags.StringVar(&options.expectDatabase, "expect-database", "", "required exact PostgreSQL database name")
	flags.DurationVar(&options.sourceTimeout, "source-timeout", options.sourceTimeout, "timeout for each legacy HTTP image")
	if options.command == "images-to-obs" {
		flags.BoolVar(&options.apply, "apply", false, "upload images and persist OBS metadata")
	}
	if options.command != "schema" && options.command != "images-to-obs" && options.command != "verify" && options.command != "report" {
		return commandOptions{}, fmt.Errorf("unsupported command %q", options.command)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return commandOptions{}, err
	}
	if flags.NArg() != 0 {
		return commandOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.reportPath == "" {
		return commandOptions{}, errors.New("--report is required (use - for stdout)")
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(options.expectDatabase) {
		return commandOptions{}, errors.New("--expect-database must be a simple PostgreSQL identifier")
	}
	if options.sourceTimeout <= 0 {
		return commandOptions{}, errors.New("--source-timeout must be positive")
	}
	return options, nil
}

func configureMigrationLogging() {
	common.LogWriterMu.Lock()
	gin.DefaultWriter = os.Stderr
	gin.DefaultErrorWriter = os.Stderr
	common.LogWriterMu.Unlock()
	gormlogger.Default = gormlogger.New(stdlog.New(os.Stderr, "\r\n", stdlog.LstdFlags), gormlogger.Config{
		SlowThreshold: 200 * time.Millisecond,
		LogLevel:      gormlogger.Warn,
		Colorful:      false,
	})
}

func requireExpectedDatabase(expected string) (string, error) {
	var current string
	if err := model.DB.Raw("SELECT current_database()").Scan(&current).Error; err != nil {
		return "", fmt.Errorf("resolve current PostgreSQL database: %w", err)
	}
	if current != expected {
		return "", fmt.Errorf("connected database %q does not match --expect-database %q", current, expected)
	}
	return current, nil
}

func requireObjectColumns() error {
	required := []struct {
		model  interface{}
		column string
	}{
		{&model.MujianImageGeneration{}, "result_object_key"},
		{&model.MujianImageGeneration{}, "result_size_bytes"},
		{&model.MujianImageGeneration{}, "result_sha256"},
		{&model.MujianImageGeneration{}, "result_etag"},
		{&model.MujianImageReference{}, "object_key"},
		{&model.MujianImageReference{}, "object_size_bytes"},
		{&model.MujianImageReference{}, "object_sha256"},
		{&model.MujianImageReference{}, "object_etag"},
		{&model.MujianShot{}, "result_object_key"},
		{&model.MujianShot{}, "result_size_bytes"},
		{&model.MujianShot{}, "result_sha256"},
		{&model.MujianShot{}, "result_etag"},
		{&model.MujianObjectOperation{}, "object_key"},
		{&model.MujianObjectOperation{}, "status"},
		{&model.MujianObjectOperation{}, "etag"},
	}
	for _, item := range required {
		if !model.DB.Migrator().HasColumn(item.model, item.column) {
			return fmt.Errorf("database column %s is missing; start the compatibility release once to run AutoMigrate", item.column)
		}
	}
	return nil
}
