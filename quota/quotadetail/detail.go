// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package quotadetail — величины отказа учёта на пути от производителя к клиенту.
//
// # Предмет
//
// Отказ по исчерпанию предела производит ОДИН производитель на платформу —
// `kacho_quota_refuse`, рендерящийся каждому владельцу учёта из общего шаблона
// `pkg/quota/refusal.sql.tmpl`. Он уже посчитал величины и положил их в `DETAIL`
// объектом JSON: носителя, вид, предел, занятое. До задачи продукта #1605 они
// терялись на первом же переходе в Go — мост SQLSTATE→sentinel сохранял
// `Message` и не читал `Detail` ни у одного из шести владельцев, — и клиент,
// получив `RESOURCE_EXHAUSTED`, мог узнать предел только разбором прозы.
//
// `api-conventions.md` §By-lane code-split требует обратного: клиент различает
// полосы МАШИННО, по `reason`-токену и полям `google.rpc.ErrorInfo`, а не
// разбором текста. У `ErrorInfo` есть штатное поле `metadata` ровно под такие
// величины — контракт менять не требуется.
//
// # Почему разбор ЗДЕСЬ, а не у каждого владельца
//
// Тот же довод, которым обоснован единственный производитель (шапка
// `pkg/quota/refusal.go`): шесть копий разбора разошлись бы молча — на имени
// ключа, на форме числа, на трактовке отсутствующей величины, — и увидеть
// расхождение можно было бы только положив копии рядом, чего не делает ни обзор
// изменения, ни прогон.
//
// # Почему пакет ЛИСТОВОЙ и без зависимостей
//
// Разбор нужен на ДВУХ слоях: у хранилища, где ещё есть `*pgconn.PgError`, и на
// пути наружу, где собирается `ErrorInfo`. Корневой `pkg/quota` тянет pgx;
// импортировать его из слоя приложения значило бы завести туда адаптер
// хранилища (`architecture.md` §Clean Architecture). Здесь — только stdlib.
//
// # Почему словарь ключей ЗАКРЫТ
//
// Наружу уходит ровно то, что объявлено ниже, и ничего сверх. Это не
// аккуратность: `DETAIL` заполняет не только наш производитель — Postgres
// вписывает туда значения строки на нарушениях ограничений («Key (name)=(…)»).
// Открытый проброс сделал бы из метаданных неконтролируемый канал из базы к
// арендатору, то есть ровно ту утечку, которую запрещает `security.md`
// §Hardening #1. Незнакомый ключ здесь не «пропускается на всякий случай» — он
// не существует.
package quotadetail

import (
	"encoding/json"
	"errors"
	"strconv"
)

// Ключи метаданных — ДОСЛОВНО имена, которыми их назвал производитель.
//
// Своих имён здесь не заводится намеренно: переименование завело бы второе
// наименование одного предмета, и разойтись эти два имени могли бы только молча
// — шаблон правится в одном месте, а перевод имён жил бы в другом.
const (
	KeyCarrierType = "carrier_type"
	KeyCarrierID   = "carrier_id"
	KeyKind        = "kind"
	KeyLimit       = "limit"
	KeyUsed        = "used"
)

// RefusalDetail — величины, посчитанные производителем отказа учёта.
//
// `Limit` и `Used` — указатели, и это несущее решение, а не стиль: ноль есть
// ЗАКОННАЯ величина занятого, поэтому «величина не названа» обязано быть
// отличимо от «величина равна нулю». Полоса «потолок не назван» (`KQ002`)
// величин не несёт вовсе — у неназванного предела занятого не существует, — и
// подставить туда ноль значило бы сообщить арендатору число, которого никто не
// считал.
type RefusalDetail struct {
	// CarrierType — вид носителя учёта («project» и прочие). Называется, а не
	// подразумевается: у владельца, считающего вид в родительском ресурсе,
	// «project» было бы прямой неправдой.
	CarrierType string
	// CarrierID — носитель, у которого кончилось место.
	CarrierID string
	// Kind — вид ресурса, как его называет каталог платформы.
	Kind string
	// Limit — предел; nil означает «не назван».
	Limit *int64
	// Used — занятое; nil означает «не названо», 0 — «названо и равно нулю».
	Used *int64
}

// Metadata отдаёт величины в форме `google.rpc.ErrorInfo.metadata`.
//
// Неназванная величина ключа НЕ ПОЛУЧАЕТ: отсутствие ключа и есть форма, в
// которой `map<string,string>` выражает отсутствие. Пустая строка на месте
// значения означала бы «величина названа и пуста».
//
// nil при полном отсутствии величин — чтобы вызывающий отличал «метаданных нет»
// от «метаданные есть и пусты», не заводя второго признака.
func (d RefusalDetail) Metadata() map[string]string {
	md := make(map[string]string, 5)
	if d.CarrierType != "" {
		md[KeyCarrierType] = d.CarrierType
	}
	if d.CarrierID != "" {
		md[KeyCarrierID] = d.CarrierID
	}
	if d.Kind != "" {
		md[KeyKind] = d.Kind
	}
	if d.Limit != nil {
		md[KeyLimit] = strconv.FormatInt(*d.Limit, 10)
	}
	if d.Used != nil {
		md[KeyUsed] = strconv.FormatInt(*d.Used, 10)
	}
	if len(md) == 0 {
		return nil
	}
	return md
}

// rawDetail — форма, в которой производитель кладёт величины: результат
// `jsonb_build_object(...)::text`.
//
// `json.Number` вместо `int64` намеренно: разбор через `float64` (умолчание
// `encoding/json` для чисел) теряет точность на величинах за пределами 2^53, а
// предел объявляется администратором и ничем сверху не ограничен.
type rawDetail struct {
	CarrierType string       `json:"carrier_type"`
	CarrierID   string       `json:"carrier_id"`
	Kind        string       `json:"kind"`
	Limit       *json.Number `json:"limit"`
	Used        *json.Number `json:"used"`
}

// Decode разбирает `DETAIL` производителя; ok=false означает «пригодных величин
// нет».
//
// Отсутствующая, не-JSON или пустая по составу `DETAIL` — НЕ отказ и не ошибка:
// отказ по пределу остаётся отказом по пределу, у него просто нет величин.
// Возвращать здесь ошибку значило бы дать вызывающему повод превратить отказ
// арендатора во внутренний.
func Decode(detail string) (RefusalDetail, bool) {
	if detail == "" {
		return RefusalDetail{}, false
	}
	var raw rawDetail
	if err := json.Unmarshal([]byte(detail), &raw); err != nil {
		return RefusalDetail{}, false
	}

	d := RefusalDetail{
		CarrierType: raw.CarrierType,
		CarrierID:   raw.CarrierID,
		Kind:        raw.Kind,
		Limit:       parseAmount(raw.Limit),
		Used:        parseAmount(raw.Used),
	}
	if d.Metadata() == nil {
		// Объект разобрался, но ни одной ОБЪЯВЛЕННОЙ величины не нёс. Это не
		// наша `DETAIL`; сказать «величины есть» было бы неправдой.
		return RefusalDetail{}, false
	}
	return d, true
}

// parseAmount переводит число производителя в величину; nil на входе и негодное
// значение дают одно и то же — «величина не названа».
func parseAmount(n *json.Number) *int64 {
	if n == nil {
		return nil
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

// detailedRefusal — ПРОЗРАЧНАЯ обёртка: несёт величины и не трогает ни текста,
// ни цепочки.
//
// Текст отказа — часть контракта (`api-conventions.md` §Error-format), и все
// шесть владельцев снимают с него префикс sentinel'а разбором `Error()`.
// Поэтому `Error()` здесь отдаёт текст вложенного ДОСЛОВНО, а `Unwrap()`
// сохраняет цепочку: неизменность текста и работа `errors.Is` держатся
// ПОСТРОЕНИЕМ, а не договорённостью и не пробой.
type detailedRefusal struct {
	err    error
	detail RefusalDetail
}

func (e *detailedRefusal) Error() string { return e.err.Error() }
func (e *detailedRefusal) Unwrap() error { return e.err }

// Attach приклеивает к уже собранному отказу величины из `DETAIL` производителя.
//
// Зовётся мостом SQLSTATE→sentinel владельца — там, где `*pgconn.PgError` ещё не
// потерян. Ошибка возвращается НЕИЗМЕНЁННОЙ, когда прикреплять нечего: пустая
// обёртка была бы утверждением «величины есть» при их отсутствии.
func Attach(err error, detail string) error {
	if err == nil {
		return nil
	}
	d, ok := Decode(detail)
	if !ok {
		return err
	}
	return &detailedRefusal{err: err, detail: d}
}

// FromError достаёт величины из цепочки; ok=false означает «их там нет».
//
// Зовётся на пути наружу, где собирается `ErrorInfo`. Цепочка к этому моменту
// цела by construction: тем же `errors.Is` по ней ходит выбор кода и признака —
// потеряйся она, отказ давно приходил бы клиенту неопознанным.
func FromError(err error) (RefusalDetail, bool) {
	var d *detailedRefusal
	if errors.As(err, &d) {
		return d.detail, true
	}
	return RefusalDetail{}, false
}

// MetadataFromError — величины ошибки в форме `google.rpc.ErrorInfo.metadata`;
// nil, когда их нет.
//
// Существует затем, чтобы место сборки ответа у каждого владельца оставалось
// ОДНОЙ строкой. Шесть собственных трёхстрочных помощников об одном предмете
// разошлись бы молча — на трактовке отсутствия величин, — и это ровно тот довод,
// которым обоснован единственный производитель отказа.
func MetadataFromError(err error) map[string]string {
	d, ok := FromError(err)
	if !ok {
		return nil
	}
	return d.Metadata()
}
