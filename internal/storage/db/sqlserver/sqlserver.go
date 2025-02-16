// Copyright 2021-2022 Zenauth Ltd.
// SPDX-License-Identifier: Apache-2.0

package sqlserver

import (
	"context"
	"database/sql"
	"fmt"

	// Import the mssql driver.
	_ "github.com/denisenkom/go-mssqldb"
	"github.com/doug-martin/goqu/v9"

	// Import the mssql dialect.
	_ "github.com/doug-martin/goqu/v9/dialect/sqlserver"
	"github.com/jmoiron/sqlx"

	"github.com/cerbos/cerbos/internal/config"
	"github.com/cerbos/cerbos/internal/observability/logging"
	"github.com/cerbos/cerbos/internal/policy"
	"github.com/cerbos/cerbos/internal/storage"
	"github.com/cerbos/cerbos/internal/storage/db/internal"
)

const DriverName = "sqlserver"

var _ storage.MutableStore = (*Store)(nil)

func init() {
	storage.RegisterDriver(DriverName, func(ctx context.Context) (storage.Store, error) {
		conf := &Conf{}
		if err := config.GetSection(conf); err != nil {
			return nil, err
		}

		return NewStore(ctx, conf)
	})
}

func NewStore(ctx context.Context, conf *Conf) (*Store, error) {
	log := logging.FromContext(ctx).Named("sqlserver")
	log.Info("Initialising SQL Server storage")

	db, err := sqlx.Connect(DriverName, conf.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	conf.ConnPool.Configure(db)

	dbStorage, err := internal.NewDBStorage(ctx, goqu.New("sqlserver", db), internal.WithUpsertPolicy(upsertPolicy), internal.WithUpsertSchema(upsertSchema))
	if err != nil {
		return nil, err
	}

	return &Store{DBStorage: dbStorage}, nil
}

type Store struct {
	internal.DBStorage
}

func (s *Store) Driver() string {
	return DriverName
}

func upsertPolicy(ctx context.Context, tx *goqu.TxDatabase, p policy.Wrapper) error {
	stm, err := tx.Prepare(`
UPDATE dbo.[policy] WITH (UPDLOCK, SERIALIZABLE) SET "definition"=@definition, "description"=@description,"disabled"=@disabled,"kind"=@kind,"name"=@name,"version"=@version where [id] = @id 
IF @@ROWCOUNT = 0
BEGIN
  INSERT INTO dbo.[policy] ("definition", "description", "disabled", "kind", "name", "version", "id") VALUES (@definition, @description, @disabled, @kind, @name, @version, @id)
END
`)
	if err != nil {
		return fmt.Errorf("failed to prepare policy upsert %s: %w", p.FQN, err)
	}

	defer stm.Close()

	definition, err := internal.PolicyDefWrapper{Policy: p.Policy}.Value()
	if err != nil {
		return fmt.Errorf("failed to get definition value: %w", err)
	}

	id, _ := p.ID.Value()

	_, err = stm.ExecContext(ctx, sql.Named("definition", definition),
		sql.Named("description", p.Description), sql.Named("disabled", p.Disabled),
		sql.Named("kind", p.Kind), sql.Named("name", p.Name), sql.Named("version", p.Version), sql.Named("id", int64(id.(uint64))))

	return err
}

func upsertSchema(ctx context.Context, tx *goqu.TxDatabase, schema internal.Schema) error {
	stm, err := tx.Prepare(`
UPDATE dbo.[attr_schema_defs] WITH (UPDLOCK, SERIALIZABLE) SET "definition"=@definition WHERE [id] = @id 
IF @@ROWCOUNT = 0
BEGIN
  INSERT INTO dbo.[attr_schema_defs] ("definition", "id") VALUES (@definition, @id)
END
`)
	if err != nil {
		return fmt.Errorf("failed to prepare schema upsert %s: %w", schema.ID, err)
	}

	defer stm.Close()

	definition, err := schema.Definition.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal defJson: %w", err)
	}
	_, err = stm.ExecContext(ctx, sql.Named("definition", definition), sql.Named("id", schema.ID))

	return err
}
