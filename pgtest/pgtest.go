// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package pgtest gives a test binary ONE Postgres and hands each test its own
// database cloned from a migrated template.
//
// # The subject
//
// A test that needs a real database used to start a real container and replay the
// whole migration chain, per test. The cost is not the tests, it is the harness:
// a package calling such a helper 91 times pays 91 container starts and 91 chain
// replays before a single assertion runs. Two packages in this tree grew past the
// 600s that `go test` applies by default, which means the literal command
// `go test ./...` could not finish them at ALL — not slowly, not at all — however
// idle the machine.
//
// This package is the generalisation of the fix already landed by hand in
// services/iam/internal/repo/kaname/pg, services/nlb/internal/repo/kacho/pg and
// services/vpc/internal/repo — three byte-for-byte copies of the same 150 lines.
// It is not a new invention; it is those three with the service-specific parts
// (the name, and how the schema is applied) lifted into Config.
//
// # Why a clone is still isolation
//
// The container starts once, the schema is applied once into a TEMPLATE database,
// and each caller gets its own database created with `CREATE DATABASE … TEMPLATE t`.
// Postgres builds that by copying the template's files, so it is cheap while
// remaining a genuinely separate database: separate catalog, separate rows,
// separate sequences, separate advisory-lock space. Nothing crosses between two
// tests that did not cross between two containers. The proofs that depend on real
// contention — CAS, UNIQUE, EXCLUDE, FOR UPDATE SKIP LOCKED — are unchanged,
// because concurrent goroutines inside ONE test still contend on the same real
// rows of the same real database.
//
// What DID change is worth stating rather than leaving to be discovered: two
// different tests can no longer collide, because they never could — they had
// separate containers before and have separate databases now. A test that passed
// only because it had the server to itself still has the database to itself. A
// test that depended on being alone with the SERVER — on `pg_stat_activity` showing
// nothing else, on a restart, on a cluster-wide setting — does not, and such a test
// must keep its own container and say why.
//
// # Why the container starts lazily
//
// Starting it from TestMain would pay for a container in every run where every test
// skips — which is what `-short` does to exactly these tests. Starting it on first
// use means a package needs no reasoning about `-short` at all: no caller, no
// container, no cost. The three hand-written copies each had to argue this in a
// comment; here it is structural.
//
// A missing Docker daemon FAILS, it does not skip. That is the posture the per-test
// helpers had (a failed container start failed the test) and inverting it would turn
// "the integration proofs did not run" into a green report.
package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for template + admin work
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Config describes the one container a package wants.
type Config struct {
	// Name is a short service identifier used to name the databases this package
	// creates (admin, template, and the per-test clones). It only has to be unique
	// inside the container, which is per-package, so the service name is enough.
	Name string

	// Migrate applies the schema to the TEMPLATE database, exactly once per test
	// binary. It is given a DSN pointing at the template. Use Goose for the usual
	// case of an embedded goose directory; supply a function directly when the
	// package builds its schema some other way (a raw CREATE TABLE, for instance).
	//
	// Nil means the template stays empty — the clones then differ from NewEmptyDB
	// only in name, which is a legitimate but unusual choice, so it is allowed
	// rather than rejected.
	Migrate func(ctx context.Context, dsn string) error

	// User and Password default to the Name when empty. They exist because the
	// helpers this replaces each picked their own, and a DSN that a test inspects
	// should not change under it.
	User, Password string

	// Image defaults to postgres:16-alpine, the image every replaced helper used.
	Image string

	// SearchPath — значение клаузы `search_path`, которое получает КАЖДЫЙ DSN,
	// выданный этим пакетом (`kaname,public`). Пусто — приведение не
	// дописывается, и поведение прежнее.
	//
	// Предмет принадлежит выдающему базу, а не спрашивающему её: схему создаёт
	// `Migrate` этого же `Config`, и кто её создал, тот и знает её имя. Пока
	// клаузу приписывал вызывающий, её приписывали 29 файлов в 25 пакетах —
	// каждый своей копией, и копии уже разошлись формой (`const`, `+=`,
	// вычисленный разделитель, проверка на удвоение — то есть, то нет).
	//
	// Забывший её получал `relation "roles" does not exist` — отказ, неотличимый
	// ни от непринятых миграций, ни от неверного имени таблицы в продукте, то
	// есть дефект ПРОБЫ, наказанный сообщением о дефекте ПРОДУКТА.
	SearchPath string
}

// Goose returns a Migrate function that replays an embedded goose directory.
func Goose(fsys fs.FS) func(context.Context, string) error {
	return func(_ context.Context, dsn string) error {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return fmt.Errorf("open template: %w", err)
		}
		goose.SetBaseFS(fsys)
		if err := goose.SetDialect("postgres"); err != nil {
			_ = db.Close()
			return fmt.Errorf("goose dialect: %w", err)
		}
		if err := goose.Up(db, "."); err != nil {
			_ = db.Close()
			return fmt.Errorf("migrate template: %w", err)
		}
		// Closed before returning: Postgres refuses `CREATE DATABASE … TEMPLATE t`
		// while anything is still connected to t.
		if err := db.Close(); err != nil {
			return fmt.Errorf("close template: %w", err)
		}
		return nil
	}
}

// SQL returns a Migrate function that executes one or more statements.
func SQL(stmts ...string) func(context.Context, string) error {
	return func(ctx context.Context, dsn string) error {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return fmt.Errorf("open template: %w", err)
		}
		defer func() { _ = db.Close() }()
		for i, s := range stmts {
			if _, err := db.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("template statement %d: %w", i, err)
			}
		}
		return nil
	}
}

// state is the one container this test binary owns, plus what it takes to hand out
// databases on it. There is one per process because a test binary is one package.
type state struct {
	cfg Config

	once     sync.Once
	baseDSN  string // points at the admin database
	template string
	startErr error

	terminate func()
	seq       atomic.Int64
}

var current struct {
	mu sync.Mutex
	s  *state
}

// Run is what a package's TestMain calls:
//
//	func TestMain(m *testing.M) { os.Exit(pgtest.Run(m, pgtest.Config{...})) }
//
// It does NOT start a container. The first NewDB / NewEmptyDB does; if none is
// reached — every test skipped under -short, or the package simply has no database
// test selected by -run — nothing is started and nothing is paid for.
func Run(m *testing.M, cfg Config) int {
	if cfg.Name == "" {
		fmt.Fprintln(os.Stderr, "pgtest: Config.Name is required — it names the databases")
		return 1
	}
	if cfg.User == "" {
		cfg.User = cfg.Name
	}
	if cfg.Password == "" {
		cfg.Password = cfg.Name
	}
	if cfg.Image == "" {
		cfg.Image = "postgres:16-alpine"
	}

	s := &state{cfg: cfg, template: "kacho_" + cfg.Name + "_template"}
	current.mu.Lock()
	current.s = s
	current.mu.Unlock()

	code := m.Run()

	if s.terminate != nil {
		s.terminate()
	}
	return code
}

func active(t testing.TB) *state {
	t.Helper()
	current.mu.Lock()
	s := current.s
	current.mu.Unlock()
	if s == nil {
		// Reachable only when a package uses NewDB without wiring TestMain. Fail
		// loudly: handing back a DSN pointing at nothing would surface later as an
		// unrelated connection error.
		t.Fatal("pgtest: no TestMain — the package must call pgtest.Run(m, pgtest.Config{…}) " +
			"from func TestMain before any test asks for a database")
	}
	return s
}

// start boots the container and prepares the template. Once, whatever the number of
// callers and whatever their goroutines.
func (s *state) start() {
	s.once.Do(func() {
		ctx := context.Background()
		adminDB := "kacho_" + s.cfg.Name + "_admin"

		pgc, err := postgres.Run(ctx,
			s.cfg.Image,
			postgres.WithDatabase(adminDB),
			postgres.WithUsername(s.cfg.User),
			postgres.WithPassword(s.cfg.Password),
			postgres.BasicWaitStrategies(),
		)
		if err != nil {
			s.startErr = fmt.Errorf("start postgres: %w", err)
			return
		}
		s.terminate = func() { _ = pgc.Terminate(context.Background()) }

		dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			s.startErr = fmt.Errorf("connection string: %w", err)
			return
		}
		s.baseDSN = dsn

		admin, err := sql.Open("pgx", s.baseDSN)
		if err != nil {
			s.startErr = fmt.Errorf("open admin: %w", err)
			return
		}
		defer func() { _ = admin.Close() }()

		if _, err := admin.ExecContext(ctx, createDatabaseStmt(s.template, "")); err != nil {
			s.startErr = fmt.Errorf("create template database: %w", err)
			return
		}
		if s.cfg.Migrate != nil {
			if err := s.cfg.Migrate(ctx, s.dsnFor(s.template)); err != nil {
				s.startErr = err
				return
			}
		}
	})
}

// dsnFor rewrites the container DSN to point at another database on it.
func (s *state) dsnFor(name string) string {
	u, err := url.Parse(s.baseDSN)
	if err != nil {
		// The DSN comes from the container driver; if it does not parse, the harness
		// itself is broken, and returning something plausible would hide that.
		panic(fmt.Sprintf("pgtest: unparseable container DSN %q: %v", s.baseDSN, err))
	}
	u.Path = "/" + name
	return WithSearchPath(u.String(), s.cfg.SearchPath)
}

// WithSearchPath дописывает к DSN клаузу `options` с приведением схемы.
//
// Экспортирована ради пакетов, которые собирают DSN САМИ — своим контейнером
// либо своей раскладкой баз, — и потому не проходят через `Config`. Реализация
// у приведения одна на дерево: пока её не было, клаузу собирали 29 мест, и
// копии уже разошлись формой.
//
// Собирается строкой, а не `url.Values.Encode()`, намеренно: `Encode` кодирует
// пробел как `+`, и выданный DSN перестал бы совпадать байт в байт с формой,
// которая уже проверена на живом сервере всеми прежними местами дерева.
// Экранируется при этом ЗНАЧЕНИЕ (`url.QueryEscape`), иначе запятая внутри
// `kaname,public` осталась бы голой и разделила параметры DSN.
//
// Клауза, уже стоящая в DSN, не удваивается: двух `options` в одном DSN не
// бывает — вторая либо молча замещает первую, либо отвергается драйвером, и оба
// исхода хуже, чем оставить объявленное вызывающим.
func WithSearchPath(dsn, searchPath string) string {
	if searchPath == "" || strings.Contains(dsn, "options=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "options=-c%20search_path%3D" + url.QueryEscape(searchPath)
}

// quoteIdent renders one database name as a single SQL identifier: wrapped in
// double quotes, inner quotes doubled.
//
// This is the ONLY correct shape here, because the alternative does not exist:
// CREATE/DROP DATABASE take no bind parameters — an identifier position in DDL is
// not a parameter position — so "rewrite it as $1" is not an available outcome.
// Quoting is what keeps the operand inside the statement it belongs to.
//
// It changes nothing for today's callers. Every Config.Name in the tree is a
// lowercase ASCII identifier, and quoting a lowercase identifier is semantically
// identical to leaving it bare (an unquoted identifier folds to lower case, and
// these are already there). What it removes is the dependence on that remaining
// true: the operand comes from a caller this package does not control.
//
// What holds this is ddlident_test.go, NOT a scanner's silence. gosec stopped
// reporting the concatenation here because the operand became a call rather than
// a bare identifier — a change of shape, which is not a verdict about safety.
func quoteIdent(name string) string { return pgx.Identifier{name}.Sanitize() }

// createDatabaseStmt builds the DDL that makes one database. template empty ⇒ no
// TEMPLATE clause.
func createDatabaseStmt(name, template string) string {
	stmt := `CREATE DATABASE ` + quoteIdent(name)
	if template != "" {
		stmt += ` TEMPLATE ` + quoteIdent(template)
	}
	return stmt
}

// dropDatabaseStmt builds the DDL that removes one database.
//
// WITH (FORCE) stays OUTSIDE the identifier: it evicts connections a test left
// open, and without it a leaked pool keeps the database alive for the rest of the
// run.
func dropDatabaseStmt(name string) string {
	return `DROP DATABASE IF EXISTS ` + quoteIdent(name) + ` WITH (FORCE)`
}

// NewDB returns a DSN for this test's own database, cloned from the migrated
// template, dropped when the test ends.
func NewDB(t testing.TB) string { return newDatabase(t, true) }

// NewEmptyDB returns a DSN for this test's own EMPTY database. It is for tests that
// must start before the current head of the migration chain and walk forward
// themselves; everything else wants NewDB.
func NewEmptyDB(t testing.TB) string { return newDatabase(t, false) }

func newDatabase(t testing.TB, fromTemplate bool) string {
	t.Helper()

	// Короткий прогон не поднимает контейнеров — и это ОБЕЩАНИЕ, данное godoc
	// самого Run («every test skipped under -short»), а не новая политика.
	// Обещание держалось намерением каждого автора пробы, и держалось плохо:
	// в одном только пакете вердикта признак несли 2 файла из 12 при трёх-семи
	// пробах в каждом (kacho#885). Короткая джоба переставала быть короткой —
	// поднимала базы и упиралась в свой предел, а `panic: test timed out`
	// читался как дефект кода.
	//
	// Место выбрано там, где проба ПРОСИТ базу, а не в TestMain каждого пакета:
	// пакетов, зовущих Run, сорок, и признак в каждом из них — сорок мест,
	// которые разойдутся. Здесь оно одно.
	//
	// Пропуск, а не тихий успех: проба, которой не дали базу, обязана быть
	// НАЗВАНА пропущенной, иначе «не выполнилось» зачитывается в «прошло» —
	// ровно то, что запрещает дисциплина вердикта.
	if testing.Short() {
		t.Skip("pgtest: короткий прогон (-short) не поднимает Postgres; " +
			"эта проба требует базы и потому пропущена")
	}

	s := active(t)
	s.start()
	if s.startErr != nil {
		// A container that will not start is a failure, never a skip: skipping would
		// report "the integration proofs did not run" as green.
		//
		// Но и обычным отказом это подавать нельзя: у пробы нет ВЕРДИКТА, а не
		// «отрицательный вердикт». Через `t.Fatalf` весь пакет отдаёт код 1, и
		// читатель идёт искать дефект в коде, которого нет; вдобавок остальные
		// пробы пакета падают каскадом за 0.00 с по несозданному предмету —
		// двенадцать «отказов» из одной непроизошедшей причины (наблюдалось
		// дважды подряд на одном прогоне).
		//
		// Конвейер это различие ЖДЁТ: его шаг интеграции распознаёт код **75**
		// как «условие не создано» и печатает свой текст. Производителя у этого
		// исхода до сих пор не было ни в одном сервисе — объявление жило в одном
		// месте, а выход кодом — ни в одном.
		fmt.Fprintf(os.Stderr,
			"pgtest: integration Postgres unavailable: %v\n"+
				"УСЛОВИЕ НЕ СОЗДАНО: вердикта нет НИ У ОДНОЙ пробы пакета, включая успевшие пройти.\n"+
				"Это не дефект кода. Прогон недействителен и повторяется.\n", s.startErr)
		os.Exit(75)
	}

	name := fmt.Sprintf("kacho_%s_t%04d", s.cfg.Name, s.seq.Add(1))
	template := ""
	if fromTemplate {
		template = s.template
	}
	stmt := createDatabaseStmt(name, template)

	admin, err := sql.Open("pgx", s.baseDSN)
	if err != nil {
		t.Fatalf("pgtest: open admin: %v", err)
	}
	defer func() { _ = admin.Close() }()

	if _, err := admin.Exec(stmt); err != nil {
		t.Fatalf("pgtest: create database %s: %v", name, err)
	}

	t.Cleanup(func() {
		drop, derr := sql.Open("pgx", s.baseDSN)
		if derr != nil {
			return
		}
		defer func() { _ = drop.Close() }()
		_, _ = drop.Exec(dropDatabaseStmt(name))
	})

	return s.dsnFor(name)
}
