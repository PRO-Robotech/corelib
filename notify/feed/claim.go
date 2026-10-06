// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
)

// Claim арендует пачку строк (замысел NTF-1 З8) — тонкий вызывающий ядра:
// границы входа — до первого оператора SQL; взятие — claimTx в своей
// транзакции, соединение возвращается пулу до расшифровки; расшифровка —
// finishClaim после фиксации, строка с неоткрывшимся секретом закрывается
// INVALID и в ответ не попадает.
func (s *Server) Claim(ctx context.Context, req *notifyv1.ClaimRequest) (*notifyv1.ClaimResponse, error) {
	classes, err := claimClasses(req)
	if err != nil {
		return nil, err
	}
	got, err := s.e.claim(ctx, classes, int64(req.GetMax()))
	if err != nil {
		return nil, s.internalError(ctx, "claim", err)
	}
	// Отсчёт времени ответа — от возврата оператора: расшифровка и закрытие
	// неоткрывшихся строк идут после него.
	elapsed := s.clock.Start()
	kept, err := s.e.finishClaim(ctx, got)
	if err != nil {
		return nil, s.internalError(ctx, "claim attrs", err)
	}
	out := make([]*notifyv1.ClaimedNotification, 0, len(kept))
	for _, r := range kept {
		out = append(out, &notifyv1.ClaimedNotification{
			Id:         r.id,
			LeaseToken: r.token,
			Template:   r.template,
			SchemaRev:  r.rev,
			Class:      wireClass(r.class),
			Recipient:  &notifyv1.ClaimedNotification_Address{Address: r.address},
			Attrs:      r.attrs,
			EnqueuedAt: timestamppb.New(r.enqueued),
		})
	}
	// Остатки — за вычетом времени, прошедшего в процессе до ответа (УК71).
	// Срок есть у каждой строки, которую выдаёт сеть: классы сети — классы
	// со сроком (CHECK expiry_matches_class).
	spent := elapsed()
	for i, n := range out {
		n.LeaseRemaining = durationpb.New(max(kept[i].leaseLeft-spent, 0))
		if left := kept[i].expiresLeft; left != nil {
			n.ExpiresIn = durationpb.New(max(*left-spent, 0))
		}
	}
	return &notifyv1.ClaimResponse{Notifications: out}, nil
}

// claimClasses — границы Claim (NTF1-B21, B24): max в [1..MaxClaim], набор
// классов непуст и из перечня контракта. Возвращает набор словами колонки
// class.
func claimClasses(req *notifyv1.ClaimRequest) ([]string, error) {
	switch m := req.GetMax(); {
	case m == 0:
		return nil, fieldError("max", "required")
	case m > MaxClaim:
		return nil, fieldError("max", fmt.Sprintf("must be ≤ %d", MaxClaim))
	}
	if len(req.GetClasses()) == 0 {
		return nil, fieldError("classes", "required")
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range req.GetClasses() {
		word, ok := classWord(c)
		if !ok {
			return nil, fieldError("classes", c.String()+" is not a class")
		}
		if !seen[word] {
			seen[word] = true
			out = append(out, word)
		}
	}
	return out, nil
}

// classWord — слово колонки class для класса контракта. Классов
// LocalOnlyClasses в контракте нет, и сеть их не выдаёт.
func classWord(c notifyv1.NotificationClass) (string, bool) {
	switch c {
	case notifyv1.NotificationClass_SECURITY:
		return string(ClassSecurity), true
	case notifyv1.NotificationClass_NOTICE:
		return string(ClassNotice), true
	case notifyv1.NotificationClass_NOTIFICATION_CLASS_UNSPECIFIED:
		return "", false
	}
	return "", false
}

func wireClass(c Class) notifyv1.NotificationClass {
	switch c {
	case ClassSecurity:
		return notifyv1.NotificationClass_SECURITY
	case ClassNotice:
		return notifyv1.NotificationClass_NOTICE
	case ClassObligation:
		// Класса нет в контракте: строку этого класса Claim сети не выдаёт.
		return notifyv1.NotificationClass_NOTIFICATION_CLASS_UNSPECIFIED
	}
	return notifyv1.NotificationClass_NOTIFICATION_CLASS_UNSPECIFIED
}
