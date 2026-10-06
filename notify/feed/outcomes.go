// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/PRO-Robotech/corelib/notify/feed/schema"
)

// Kind — вид исхода строки ленты (Р11): слово контракта в нижнем регистре.
// Терминальный вид совпадает с состоянием строки; defer состоянием не бывает
// — строка остаётся pending.
type Kind string

// Виды исхода.
const (
	KindSent              Kind = "sent"
	KindRecipientRejected Kind = "recipient_rejected"
	KindDefer             Kind = "defer"
	KindDenied            Kind = "denied"
	KindInvalid           Kind = "invalid"
	KindDropped           Kind = "dropped"
	KindExpired           Kind = "expired"
	// KindSuppressed — письмо подавлено notify (Х3 NTF-4, Р17): терминальный
	// исход с причиной из закрытого перечня; вклад строки в окна лимита
	// источника не возвращается.
	KindSuppressed Kind = "suppressed"
)

// Reason — причина исхода (словарь Р11). Пустое значение — «причины нет»: так
// у sent и recipient_rejected.
type Reason string

// Причины исхода.
const (
	ReasonNone                    Reason = ""
	ReasonPlatformUnavailable     Reason = "platform_unavailable"
	ReasonGrantSkew               Reason = "grant_skew"
	ReasonTemplateSkew            Reason = "template_skew"
	ReasonRecipientNet            Reason = "recipient_net"
	ReasonRevoked                 Reason = "revoked"
	ReasonAttrsInvalid            Reason = "attrs_invalid"
	ReasonClassMismatch           Reason = "class_mismatch"
	ReasonRecipientInvalid        Reason = "recipient_invalid"
	ReasonClassNotAllowed         Reason = "class_not_allowed"
	ReasonRecipientFormNotAllowed Reason = "recipient_form_not_allowed"
	ReasonSealedMismatch          Reason = "sealed_mismatch"
	ReasonKeyUnavailable          Reason = "key_unavailable"
	ReasonUnclaimed               Reason = "unclaimed"
	ReasonNoAck                   Reason = "no_ack"
	ReasonHardBounce              Reason = "hard_bounce"
	ReasonSoftBounce              Reason = "soft_bounce"
	ReasonComplaint               Reason = "complaint"
	ReasonUnsubscribe             Reason = "unsubscribe"
)

// Outcome — исход строки: вид и причина. Равенство исходов — равенство пары
// (CX1-26 (в)); defer_for в него не входит.
type Outcome struct {
	Kind   Kind
	Reason Reason
}

// Classes — закрытый перечень классов (метка class).
func Classes() []Class { return []Class{ClassSecurity, ClassNotice} }

// Kinds — закрытый перечень видов исхода (метка kind).
func Kinds() []Kind {
	return []Kind{KindSent, KindRecipientRejected, KindDefer, KindDenied, KindInvalid, KindDropped, KindExpired, KindSuppressed}
}

// Reasons — закрытый перечень причин (метка reason), выведенный из ЕДИНСТВЕННОЙ
// таблицы «состояние × причина» схемы (schema.OutcomePairs): второго перечня
// нет, и CHECK outcome_pair, сервер ленты и метрики читают одно.
func Reasons() []Reason {
	seen := map[Reason]bool{}
	for _, rs := range schema.OutcomePairs() {
		for _, r := range rs {
			seen[Reason(r)] = true
		}
	}
	out := make([]Reason, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Кто ставит причину, кроме Ack. Эти причины Ack не принимает: их ставит сама
// лента — сервер (шифротекст не открылся) и уборщик (строку не выдали либо
// исход не записан). Из таблицы схемы они не вычитаются — колонку пишут.
var (
	serverOnly  = []Reason{ReasonSealedMismatch, ReasonKeyUnavailable}
	sweeperOnly = []Reason{ReasonUnclaimed, ReasonNoAck}
)

// ackReasons — допустимые причины Ack по виду. Терминальные виды берут причины
// своего состояния из таблицы схемы без причин, которые ставит лента; DEFER —
// причины EXPIRED без причин уборщика: EXPIRED наследует причину последней
// отсрочки, и множества совпадают по построению. Вид без причин (sent,
// recipient_rejected) допускает только ReasonNone; EXPIRED в Ack не бывает.
func ackReasons() map[Kind][]Reason {
	pairs := schema.OutcomePairs()
	strip := func(rs []string, drop []Reason) []Reason {
		var out []Reason
		for _, r := range rs {
			if !slices.Contains(drop, Reason(r)) {
				out = append(out, Reason(r))
			}
		}
		return out
	}
	out := map[Kind][]Reason{KindDefer: strip(pairs[string(KindExpired)], sweeperOnly)}
	for _, k := range []Kind{KindSent, KindRecipientRejected, KindDenied, KindInvalid, KindDropped, KindSuppressed} {
		rs := strip(pairs[string(k)], serverOnly)
		if len(rs) == 0 {
			rs = []Reason{ReasonNone}
		}
		out[k] = rs
	}
	return out
}

// RefundReasons — причины истечения, при которых вклад строки в окна
// возвращается (CX1-53 (в)): письмо не уходило по вине платформы либо не было
// выдано вовсе. Объявлены однажды: из них строится оператор уборщика и
// предикат LimitRefundedPredicate.
func RefundReasons() []Reason { return []Reason{ReasonPlatformUnavailable, ReasonUnclaimed} }

var aliasForm = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// LimitRefundedPredicate — фрагмент SQL «лимит этой строки ленты возвращён»
// над строкой с псевдонимом alias (УК68, заказ NTF-2 Е8 (а)). Выведен из
// RefundReasons. Удалённую уборкой строку читатель толкует сам; срок хранения
// закрытых строк — ClosedRetention.
func LimitRefundedPredicate(alias string) (string, error) {
	if !aliasForm.MatchString(alias) {
		return "", fmt.Errorf("feed: псевдоним %q — не идентификатор SQL в нижнем регистре", alias)
	}
	words := make([]string, 0, len(RefundReasons()))
	for _, r := range RefundReasons() {
		words = append(words, "'"+string(r)+"'")
	}
	return fmt.Sprintf("(%[1]s.state = '%[2]s' AND %[1]s.outcome_reason IN (%[3]s))",
		alias, KindExpired, strings.Join(words, ", ")), nil
}

// PutDefect — причина дефекта программы вызывающего, отвергнутого Put
// (метка cause, CX1-67).
type PutDefect string

// PutDefectSecondLimitedPut — вторая постановка с лимитами в одной транзакции.
const PutDefectSecondLimitedPut PutDefect = "second_limited_put"

// PutDefects — закрытый перечень причин дефекта.
func PutDefects() []PutDefect { return []PutDefect{PutDefectSecondLimitedPut} }
