// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package platformmodules — ЕДИНСТВЕННОЕ объявление того, как один и тот же
// модуль платформы называется в трёх разных местах.
//
// # Написаний ТРИ, и они не выводятся друг из друга
//
//	короткое имя службы   каталог `services/<X>`, короткое имя SAN его mTLS
//	модуль каталога       сегмент пакета контрактов `proto/kacho/cloud/<Y>/`
//	домен типов объекта   приставка типов модели прав `<Z>_`
//
// У большинства модулей все три совпадают. У балансировщика РАЗЛИЧНЫ ДВА из
// трёх: служба `nlb`, контракт `loadbalancer`, типы `nlb_listener`. Соответствие
// установлено решением, а не правилом, и вывести одно написание из другого
// нельзя ничем.
//
// # Почему это отдельное объявление в продукте, а не литерал у каждого читателя
//
// Замер (#1885): в корпусе гейтов соответствие «служба ↔ модуль каталога» было
// выписано ПЯТЬЮ независимыми копиями, и каждая объясняла в комментарии, почему
// вывести его нельзя, — пять раз об одном предмете. В продуктовом дереве его не
// было НИ ОДНОГО, поэтому «чей это тип» отвечала приставка имени, то есть
// соглашение об именовании.
//
// Повторённая величина расходится молча. Здесь она одна, и КАЖДАЯ ЕЁ КОЛОНКА —
// проверяемый факт, а не заявление: гейт дерева
// TestPlatformModuleVocabularyMatchesTheTree сверяет короткое имя с каталогом
// служб, модуль каталога — с деревом контрактов, домен типов — с моделью прав, и
// печатает объём осмотренного по каждой колонке.
//
// # Чего здесь НЕТ и почему
//
// Отображения «тип объекта → модуль-владелец» здесь нет: полный перечень типов
// живёт в закрытой таблице `services/iam/internal/authzmap`, за границей
// видимости пакета, и вторая его копия разошлась бы с первой молча — ровно тот
// класс, ради которого пакет и заведён. Поэтому владение проверяется приставкой
// ОБЪЯВЛЕННОГО домена, а не полным словарём типов; переход на словарь держит
// задача #1816 — вместе с формой, которой личность вызывающего приезжает.
// Эпик #1087 — её основание, а не держатель: работы эпик не производит.
package platformmodules

import "sort"

// Module — три написания одного модуля платформы.
type Module struct {
	// Service — короткое имя службы: каталог `services/<Service>` и короткое
	// имя SAN её клиентского сертификата.
	Service string
	// CatalogModule — сегмент пакета контрактов: `proto/kacho/cloud/<CatalogModule>`.
	CatalogModule string
	// ObjectDomain — приставка типов объекта модели прав (`<ObjectDomain>_`).
	// ПУСТАЯ строка — не «неизвестно», а «модель не объявляет ни одного типа
	// этого модуля»; так у geo, и это проверяемый факт, а не пропуск.
	ObjectDomain string
}

// modules — объявление. Порядок — по короткому имени службы.
var modules = []Module{
	{Service: "compute", CatalogModule: "compute", ObjectDomain: "compute"},
	// geo — каталог размещения; собственных типов объекта модель прав ему не
	// объявляет: чтение регионов и зон освобождено от project-scope, а
	// админ-CRUD гейтится отношением на кластере.
	{Service: "geo", CatalogModule: "geo", ObjectDomain: ""},
	// iam — типы его домена `iam_*`. Общие предки иерархии (`account`,
	// `project`) и типы субъектов (`user`, `service_account`, `group`) голые:
	// они не принадлежат ни одному модулю и потому под эту колонку не подпадают.
	{Service: "iam", CatalogModule: "iam", ObjectDomain: "iam"},
	// nlb — единственный модуль, у которого различны ДВА написания из трёх.
	{Service: "nlb", CatalogModule: "loadbalancer", ObjectDomain: "nlb"},
	{Service: "registry", CatalogModule: "registry", ObjectDomain: "registry"},
	{Service: "storage", CatalogModule: "storage", ObjectDomain: "storage"},
	{Service: "vpc", CatalogModule: "vpc", ObjectDomain: "vpc"},
}

// All отдаёт КОПИЮ объявления, отсортированную по короткому имени службы.
func All() []Module {
	out := make([]Module, len(modules))
	copy(out, modules)
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// CatalogModuleOfService — модуль каталога по короткому имени службы.
// ok=false — служба не объявлена.
func CatalogModuleOfService(service string) (string, bool) {
	for _, m := range modules {
		if m.Service == service {
			return m.CatalogModule, true
		}
	}
	return "", false
}

// ServiceOfCatalogModule — короткое имя службы по модулю каталога.
// ok=false — модуль не объявлен.
func ServiceOfCatalogModule(catalogModule string) (string, bool) {
	for _, m := range modules {
		if m.CatalogModule == catalogModule {
			return m.Service, true
		}
	}
	return "", false
}

// ObjectDomainOfService — приставка типов объекта по короткому имени службы.
//
// Различайте два «нет»: ok=false — службы в объявлении нет вовсе; ok=true с
// пустой строкой — служба объявлена и типов объекта у неё НЕТ. Схлопывать их в
// один ответ нельзя: вызывающий, решающий по нему о доступе, обязан отличать
// «не знаю такого» от «знаю, и типов у него нет».
func ObjectDomainOfService(service string) (string, bool) {
	for _, m := range modules {
		if m.Service == service {
			return m.ObjectDomain, true
		}
	}
	return "", false
}

// Services — короткие имена всех объявленных служб, отсортированно.
func Services() []string {
	out := make([]string, 0, len(modules))
	for _, m := range modules {
		out = append(out, m.Service)
	}
	sort.Strings(out)
	return out
}

// AliasesByService — расхождения «короткое имя службы → модуль каталога»,
// картой. Совпадающие написания в карту НЕ ПОПАДАЮТ, и это часть контракта:
// читатели таких карт трактуют отсутствие записи как «написания совпали», то
// есть совпадающая запись изменила бы смысл карты, ничего к ней не добавив.
//
// Отдана здесь, а не собирается у каждого читателя: собранная на месте, она
// была бы шестой копией того же соответствия — ровно тем, что пакет и снял.
func AliasesByService() map[string]string {
	out := map[string]string{}
	for _, m := range modules {
		if m.CatalogModule != m.Service {
			out[m.Service] = m.CatalogModule
		}
	}
	return out
}

// AliasesByCatalogModule — та же карта в обратную сторону.
func AliasesByCatalogModule() map[string]string {
	out := map[string]string{}
	for _, m := range modules {
		if m.CatalogModule != m.Service {
			out[m.CatalogModule] = m.Service
		}
	}
	return out
}
