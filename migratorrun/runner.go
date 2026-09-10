// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package migratorrun

import (
	"context"
	"database/sql"
	"io"
	"io/fs"
	"os"

	"github.com/pressly/goose/v3"

	"github.com/PRO-Robotech/corelib/dropguard"
	"github.com/PRO-Robotech/corelib/migratorcli"
)

// Config — параметры одного запуска наката. Заполняет их `cmd/migrator/main.go`
// каждой службы из разобранных аргументов, окружения и своей конфигурации.
type Config struct {
	// Service — имя службы, чью цепочку применяет накат. Обязательное: без него
	// живой счёт строк перед сносом не может назвать, что он стережёт, а
	// безымянный отказ в логе init-контейнера некому адресовать.
	Service string

	// Dialect — имя диалекта, как его назвал оператор. Пустое имя НЕ означает
	// «умолчание»: разбор аргументов подставляет умолчание сам, поэтому пустая
	// строка сюда доезжает только как явное `--dialect ""` — то есть как
	// названный и неподдерживаемый диалект.
	Dialect string

	// DSN — строка подключения. Резолвит её [migratorcli.ResolveDSN] у всех
	// семи, с одним приоритетом: `--dsn` > [migratorcli.EnvDSN] > конфигурация.
	DSN string

	// FS — файловая система с миграциями (`internal/migrations.FS` службы).
	// Параметром, а не импортом: правило `internal` языка запрещает пакету из
	// корня импортировать `services/<svc>/internal/migrations`.
	FS fs.FS

	// MigrationsDir — путь внутри FS; для корня встроенной — ".".
	MigrationsDir string

	// DSNExtraSources — чем ЭТА служба заполняет DSN СВЕРХ двух общих, в порядке
	// убывания приоритета. Два общих здесь не перечисляются: их печатает
	// [migratorcli.DSNSourceList], поэтому умолчать источник, который перебивает
	// названные, нельзя by construction.
	DSNExtraSources []string
}

// Runner — накат цепочки. Один экземпляр на жизнь процесса; параллельное
// использование не предполагается (goose держит настройку в пакетных глобалках,
// а командная строка гоняет одну команду за раз).
type Runner struct {
	cfg  Config
	spec migratorcli.DialectSpec
}

// New собирает Runner, проверив предусловия ДО первого обращения к базе.
//
// Проверки, их порядок и тексты отказа объявлены один раз на дерево —
// [migratorcli.RunnerPreconditions]. Здесь они не переобъявляются: своей
// редакции того же текста завестись негде, потому что Runner в дереве один.
func New(cfg Config) (*Runner, error) {
	spec, err := migratorcli.ResolveDialectSpec(cfg.Dialect)
	if err != nil {
		return nil, err
	}
	pre := migratorcli.RunnerPreconditions{
		Service:         cfg.Service,
		DialectSet:      true,
		DialectSpecName: spec.Name,
		DSN:             cfg.DSN,
		DSNExtraSources: cfg.DSNExtraSources,
		MigrationsFSSet: cfg.FS != nil,
		MigrationsDir:   cfg.MigrationsDir,
	}
	if err := pre.Validate(); err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg, spec: spec}, nil
}

// open настраивает goose под диалект, открывает базу с барьером готовности и
// провязывает доставку уведомлений сервера.
//
// Настройка идёт ПЕРВОЙ: она не ходит в сеть, поэтому негодная файловая система
// или незнакомый диалект отвергаются до соединения, а не после него.
//
// Соединение ОДНО на команду — им пользуются и счёт перед сносом, и сам накат.
// Делегирующая форма открывала базу дважды за один `up`; второе соединение не
// давало ничего, кроме второго прохождения барьера готовности.
//
// # Приёмник уведомлений заводится ЗДЕСЬ, и назначение у него то же, что у
// счёта перед сносом
//
// Оператор читает ОДИН поток — вывод init-контейнера, — и перепись счёта уже
// уходит в него же. Второй поток для уведомлений сделал бы один отчёт двумя, из
// которых читают обычно один (#2544).
func (r *Runner) open(ctx context.Context) (*sql.DB, *migratorcli.NoticeRelay, error) {
	if err := migratorcli.SetupGoose(r.cfg.FS, r.spec); err != nil {
		return nil, nil, err
	}
	notices := migratorcli.NewNoticeRelay(r.cfg.Service, os.Stderr)
	db, err := migratorcli.OpenDB(ctx, r.cfg.DSN, r.spec, notices)
	if err != nil {
		return nil, nil, err
	}
	return db, notices, nil
}

// closing — общий хвост команды: перепись уведомлений, затем закрытие.
//
// Перепись печатается ВСЕГДА и на ЛЮБОМ исходе, включая отказ: сказанное
// сервером до отказа — самое ценное, что есть у оператора, а «ноль доставленных»
// обязано быть отличимо от «обработчика не было вовсе». Хвост один на три
// команды, потому что три редакции одного отчёта разошлись бы молча.
func closing(db *sql.DB, notices *migratorcli.NoticeRelay) func() {
	return func() {
		notices.WriteCensus()
		_ = db.Close()
	}
}

// Up применяет цепочку — сперва СОСЧИТАВ строки в таблицах, которые уронят ещё не
// применённые миграции В ПРЕДЕЛАХ ЭТОГО ПРОГОНА, и лишь затем применяя.
// Ненулевой счёт прекращает применение.
//
// Граница прогона разбирается ДО счёта: страж обязан считать ровно те сносы,
// которые этот прогон ВЫПОЛНИТ, а не все ещё не применённые. Разбор — та же
// функция, которой читает цель goose, поэтому двух редакций одного числа
// завестись не может; заодно негодная цель отвергается до соединения с базой.
//
// Обойти счёт цель НЕ ДАЁТ, и это построение, а не обещание: незаданная цель —
// нулевое значение [dropguard.Target], то есть «считать всё», а суженная сужает
// ровно настолько же и применяемое.
func (r *Runner) Up(ctx context.Context, target string) error {
	scope := dropguard.WholeChain()
	var version int64
	if target != "" {
		v, err := migratorcli.ParseTargetVersion(target)
		if err != nil {
			return err
		}
		version, scope = v, dropguard.UpTo(v)
	}

	db, notices, err := r.open(ctx)
	if err != nil {
		return err
	}
	defer closing(db, notices)()

	// СЧЁТ ПЕРЕД СНОСОМ — здесь, и это единственное место. Шаг, который зовут
	// отдельной строкой, однажды не позовут; отсюда его не обойти, не обойдя
	// сам Up. Недоступность базы — НЕ «ноль строк»: она отказ, а не разрешение.
	//
	// Измеряющий гейт в internal/migrations отвечает на другой вопрос — сколько
	// сеет наша собственная цепочка, проигранная в пустую базу. Что записал
	// арендатор, не знает ни один контейнер, и узнать это можно только здесь.
	if err := dropguard.Gate(ctx, db, r.cfg.Service, r.cfg.FS, os.Stderr, scope); err != nil {
		return err
	}

	// ПРОПУЩЕННЫЕ МИГРАЦИИ ПРИНИМАЮТСЯ, и это не послабление, а следствие схемы
	// нумерации. Номер у нас — «задача × 1000 + порядок», и он НЕ хронологичен by
	// construction: задача закрывается не по порядку номеров, поэтому файл с
	// меньшим номером появляется в дереве позже. База, накатившая больший номер
	// раньше, при обновлении видит «пропущенную миграцию перед текущей версией»
	// и отказывает — служба не стартует вовсе. Конвейер этого не видит by
	// construction: он всегда поднимает чистую базу, где пропущенных нет.
	//
	// Приём пропущенной означает ПРИМЕНИТЬ её, а не пропустить; порядок внутри
	// одной задачи (`NNN001` до `NNN002`) goose сохраняет независимо от опции.
	if target == "" {
		return goose.UpContext(ctx, db, r.cfg.MigrationsDir, goose.WithAllowMissing())
	}
	return goose.UpToContext(ctx, db, r.cfg.MigrationsDir, version, goose.WithAllowMissing())
}

// Down откатывает цепочку: незаданная цель — один шаг назад, заданная — до
// названной версии включительно.
//
// Счёта перед сносом здесь нет намеренно: откат — заявленный снос, а не
// побочный. Предмет стража — снос, приезжающий вместе с накатом, о котором
// оператор не знал.
func (r *Runner) Down(ctx context.Context, target string) error {
	var version int64
	if target != "" {
		v, err := migratorcli.ParseTargetVersion(target)
		if err != nil {
			return err
		}
		version = v
	}

	db, notices, err := r.open(ctx)
	if err != nil {
		return err
	}
	defer closing(db, notices)()

	if target == "" {
		return goose.DownContext(ctx, db, r.cfg.MigrationsDir)
	}
	return goose.DownToContext(ctx, db, r.cfg.MigrationsDir, version)
}

// Status печатает применённые и непринятые миграции.
//
// out принимается параметром и ЗДЕСЬ НЕ ЧИТАЕТСЯ: goose v3 пишет в собственный
// логгер, а перенаправление идёт через goose.SetLogger. Параметр остаётся, чтобы
// перенаправление, когда его заведут, не меняло подпись у семи вызывающих.
func (r *Runner) Status(ctx context.Context, out io.Writer) error {
	_ = out

	db, notices, err := r.open(ctx)
	if err != nil {
		return err
	}
	defer closing(db, notices)()

	return goose.StatusContext(ctx, db, r.cfg.MigrationsDir)
}
