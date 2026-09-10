// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package quota

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

// Единый ИСТОЧНИК отказа учёта.
//
// # Почему источник, а не «одно место»
//
// У каждого владельца своя база, поэтому физически функция обязана быть у
// каждого своя: «одно место» здесь недостижимо by construction. Достижимо одно
// ИСТОЧНИК — шаблон, из которого файл миграции каждого владельца рендерится
// целиком, и гейт дерева, который перерендеривает и сравнивает побайтово.
//
// Разница с «пятью согласованными копиями» не стилистическая. Копии УЖЕ
// разошлись: два владельца называли носителя, три вписывали слово «project»
// литералом. Каждая копия по отдельности защитима, и увидеть расхождение можно
// было только положив их рядом — чего не делает ни обзор изменения, ни прогон.

//go:embed refusal.sql.tmpl
var refusalTemplate string

// RefusalOwner — владелец, которому рендерится файл.
type RefusalOwner struct {
	// Service — каталог сервиса под `services/`.
	Service string
	// Schema — имя схемы, в которой живёт таблица учёта.
	Schema string
	// Migration — имя файла миграции без каталога, ЛИБО пусто.
	//
	// Пусто означает: цепь этого владельца СВЕДЕНА, и отказ живёт в её первичной
	// миграции, а не в отдельном отрендеренном файле. Это не послабление и не
	// исключение из проверки — сверка тела функции (см. `RefusalFunctionBodies`)
	// идёт у ВСЕХ владельцев одинаково; отпадает только сверка файла целиком,
	// которой у сведённой цепи нет предмета by construction: рендерить файл в
	// такую цепь значило бы завести миграцию с версией НИЖЕ уже применённой
	// первичной, то есть версию, которую мигратор не применит вовсе.
	Migration string
}

// RendersOwnFile говорит, рендерится ли этому владельцу отдельный файл миграции.
//
// Отдельный предикат, а не сравнение с пустой строкой на месте вызова: у него два
// вызывающих — генератор и гейт, — и они обязаны решать это одинаково. Разойдись
// они, генератор писал бы файл, которого гейт не ждёт, либо наоборот.
func (o RefusalOwner) RendersOwnFile() bool { return o.Migration != "" }

// refusalOwners — КТО получает отказ учёта.
//
// Перечень выписан, а не выведен из дерева, и это осознанно: вывести его можно
// было бы только тем же поиском по «kacho_quota_refuse», который и есть предмет
// проверки, — предикат совпал бы с предметом и подтверждал бы сам себя.
//
// Владелец, заводящий учёт следующим, добавляет сюда ОДНУ строку и получает
// файл миграции рендером. Своего текста отказа он не пишет — и написать не
// может, не покраснев на гейте.
var refusalOwners = []RefusalOwner{
	// iam — владелец ВЕЛИЧИН, и он же владелец типа «аккаунт». Единственный, у
	// кого совпало и то и другое; отказ от этого не становится особенным —
	// именно поэтому он рендерится тем же шаблоном, что у остальных, а не
	// пишется здесь своими словами.
	// iam — владелец ВЕЛИЧИН, и он же владелец типа «аккаунт». Своего файла ему
	// не рендерится: цепь iam сведена в одну первичную миграцию, и отказ стоит в
	// ней. Отсюда пустое `Migration` — см. поле, там сказано, почему это не
	// послабление: тело функции сверяется у него ровно тем же способом, что у
	// остальных пяти.
	{Service: "iam", Schema: "kaname", Migration: ""},
	{Service: "vpc", Schema: "kacho_vpc", Migration: "0044_quota_refusal_single_source.sql"},
	{Service: "compute", Schema: "public", Migration: "0038_quota_refusal_single_source.sql"},
	{Service: "storage", Schema: "kacho_storage", Migration: "0025_quota_refusal_single_source.sql"},
	{Service: "nlb", Schema: "kacho_nlb", Migration: "0034_quota_refusal_single_source.sql"},
	{Service: "registry", Schema: "kacho_registry", Migration: "0017_quota_refusal_single_source.sql"},
}

// RefusalOwners отдаёт перечень владельцев в устойчивом порядке.
//
// Копия, а не сам срез: перечень — предмет гейта, и вызывающий, способный его
// изменить, сделал бы гейт зависимым от порядка вызовов.
func RefusalOwners() []RefusalOwner {
	out := make([]RefusalOwner, len(refusalOwners))
	copy(out, refusalOwners)
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// RenderRefusalMigration отдаёт содержимое файла миграции владельца.
//
// Рендер — единственный способ получить этот текст: и генератор, и гейт зовут
// ЭТУ функцию, поэтому «что сгенерировано» и «что проверяется» не могут
// разойтись. Второй рендерер сделал бы гейт проверкой самого себя.
func RenderRefusalMigration(o RefusalOwner) (string, error) {
	if o.Schema == "" {
		return "", fmt.Errorf("quota refusal: owner %q has no schema", o.Service)
	}
	if !validSchemaName(o.Schema) {
		return "", fmt.Errorf("quota refusal: schema %q is not a plain identifier", o.Schema)
	}

	tmpl, err := template.New("refusal").Parse(refusalTemplate)
	if err != nil {
		return "", fmt.Errorf("quota refusal: parse template: %w", err)
	}

	// `public` уже стоит в пути поиска, и повтор `public, public` был бы
	// бессмысленным дублем в тексте, который читает оператор.
	searchPath := o.Schema + ", public"
	if o.Schema == "public" {
		searchPath = "public"
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, struct {
		Q          string
		SearchPath string
	}{
		Q:          o.Schema + ".",
		SearchPath: searchPath,
	}); err != nil {
		return "", fmt.Errorf("quota refusal: render for %s: %w", o.Service, err)
	}
	return b.String(), nil
}
