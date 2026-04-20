package database

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

var Pool *pgxpool.Pool

// Init initializes the PostgreSQL connection pool for the Master Backend using pgx.
// It retrieves the URL from the PG_URL environment variable.
func Init(ctx context.Context) error {
	dsn := os.Getenv("PG_URL")
	if dsn == "" {
		return fmt.Errorf("PG_URL env var is not set")
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("invalid PG_URL: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}

	Pool = pool
	log.Println("[database] backend successfully connected to postgresql")

	// Auto-migration: Ensure hostname support for SNI Routing
	migrationQuery := `
		DO $$ 
		BEGIN 
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='servers' AND column_name='server_type') THEN
				ALTER TABLE servers ADD COLUMN server_type VARCHAR(20) NOT NULL DEFAULT 'proxy';
			END IF;
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='servers' AND column_name='installing') THEN
				ALTER TABLE servers ADD COLUMN installing BOOLEAN NOT NULL DEFAULT FALSE;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='servers' AND column_name='install_progress') THEN
				ALTER TABLE servers ADD COLUMN install_progress INTEGER NOT NULL DEFAULT 0;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'unique_hostname_per_node') THEN
				ALTER TABLE servers ADD CONSTRAINT unique_hostname_per_node UNIQUE (node_id, hostname);
			END IF;
			-- Ensure session tokens can hold long JWTs
			ALTER TABLE user_sessions ALTER COLUMN token TYPE TEXT;
		END $$;
	`
	_, migrationErr := Pool.Exec(ctx, migrationQuery)
	if migrationErr != nil {
		log.Printf("[database] WARNING: auto-migration failed: %v", migrationErr)
	} else {
		log.Println("[database] schema auto-migration completed successfully")
	}

	return nil
}

// Close gracefully closes the connection pool.
func Close() {
	if Pool != nil {
		Pool.Close()
	}
}
