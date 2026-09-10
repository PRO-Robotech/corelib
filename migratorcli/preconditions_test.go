// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// preconditions_test.go — предусловия наката объявлены ОДИН раз, а источник DSN
// у них параметр, а не текст.
//
// # Предмет
//
// До сведения `Config.Validate` жил тремя копиями (#1383). Пять проверок из
// шести были побайтово одинаковы, шестая — про DSN — расходилась, и расхождение
// объяснялось тем, что каждый сервис называет СВОЙ источник. Объяснение
// пережило свой предмет: после #1461 DSN у всех семи резолвит один общий
// [ResolveDSN] с приоритетом `--dsn` > [EnvDSN] > конфигурация сервиса, а
// собственный текст `nlb` называл только ТРЕТИЙ источник и умалчивал ВТОРОЙ —
// тот, что его перебивает.
//
// Отсюда форма: два всегда живых источника печатает сам пакет, сервис
// объявляет лишь то, что СВЕРХ них. Умолчать перебивающий источник больше
// нельзя by construction — его печатает не сервис.
package migratorcli_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/migratorcli"
)

// ok — предусловия, выполненные полностью. Отрицания ниже портят ровно одно
// поле каждое: без этой базы «отвергнуто» было бы неотличимо от «отвергает всё».
func ok() migratorcli.RunnerPreconditions {
	return migratorcli.RunnerPreconditions{
		Service:         "vpc",
		DialectSet:      true,
		DialectSpecName: "postgres",
		DSN:             "postgres://localhost/kacho_vpc",
		MigrationsFSSet: true,
		MigrationsDir:   ".",
	}
}

func TestRunnerPreconditionsAcceptAFullyWiredRunner(t *testing.T) {
	// Положительный контроль. Без него все проверки ниже зеленели бы на
	// реализации, отвергающей любой вход.
	if err := ok().Validate(); err != nil {
		t.Fatalf("полностью заполненные предусловия отвергнуты: %v", err)
	}
}

func TestRunnerPreconditionsRefuseEachMissingField(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spoil   func(*migratorcli.RunnerPreconditions)
		wantSub string
	}{
		{"service", func(p *migratorcli.RunnerPreconditions) { p.Service = "" },
			"service is empty"},
		{"dialect", func(p *migratorcli.RunnerPreconditions) { p.DialectSet = false },
			"dialect is not set"},
		{"spec-name", func(p *migratorcli.RunnerPreconditions) { p.DialectSpecName = "" },
			"dialect spec.Name is empty"},
		{"dsn", func(p *migratorcli.RunnerPreconditions) { p.DSN = "" },
			"dsn is empty"},
		{"fs", func(p *migratorcli.RunnerPreconditions) { p.MigrationsFSSet = false },
			"migrations FS is nil"},
		{"dir", func(p *migratorcli.RunnerPreconditions) { p.MigrationsDir = "" },
			"migrations dir is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ok()
			tc.spoil(&p)
			err := p.Validate()
			if err == nil {
				t.Fatalf("поле %q пусто, а отказа нет", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("текст отказа %q не называет %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestRunnerPreconditionsCheckDialectBeforeReadingItsSpec(t *testing.T) {
	// Порядок — часть контракта, а не вкус: вызывающий читает Spec() у диалекта,
	// и на незаданном диалекте это разыменование nil. Проверка «диалект задан»
	// обязана стоять РАНЬШЕ проверки его имени.
	p := ok()
	p.DialectSet = false
	p.DialectSpecName = ""
	err := p.Validate()
	if err == nil {
		t.Fatal("отказа нет")
	}
	if !strings.Contains(err.Error(), "dialect is not set") {
		t.Errorf("оба поля пусты, а отказ назвал не первое: %q", err.Error())
	}
}

func TestDSNRefusalAlwaysNamesTheTwoSourcesEveryServiceHas(t *testing.T) {
	// Ядро сведения. Что бы сервис ни объявил СВЕРХ, флаг и общая переменная
	// окружения названы всегда — их печатает пакет, а не сервис. Именно этого
	// не хватало тексту nlb: он умалчивал переменную, которая его перебивает.
	for _, extra := range [][]string{
		nil,
		{"config repository.postgres.url"},
		{"config repository.postgres.url", "KACHO_NLB_REPOSITORY__POSTGRES__URL"},
	} {
		p := ok()
		p.DSN = ""
		p.DSNExtraSources = extra
		err := p.Validate()
		if err == nil {
			t.Fatalf("extra=%v: отказа нет", extra)
		}
		msg := err.Error()
		if !strings.Contains(msg, "--dsn") {
			t.Errorf("extra=%v: отказ %q не называет --dsn", extra, msg)
		}
		if !strings.Contains(msg, migratorcli.EnvDSN) {
			t.Errorf("extra=%v: отказ %q не называет %s — источник, перебивающий конфигурацию",
				extra, msg, migratorcli.EnvDSN)
		}
		for _, e := range extra {
			if !strings.Contains(msg, e) {
				t.Errorf("extra=%v: отказ %q не называет объявленный источник %q", extra, msg, e)
			}
		}
	}
}
