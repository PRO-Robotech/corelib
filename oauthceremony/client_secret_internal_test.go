// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// client_secret_internal_test.go — хешер, которым церемония подключает порт
// сверки секрета к движку: он сверяет только в доказательстве клиента и ничего
// не хеширует. Файл внутренний: хешер и ведомость операции не видны снаружи.
package oauthceremony

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// countingVerifier — порт сверки, считающий вызовы и отвечающий «совпал».
type countingVerifier struct {
	mu    sync.Mutex
	calls []string
}

func (v *countingVerifier) VerifyClientSecret(_ context.Context, clientID string, _ PresentedSecret) (SecretVerdict, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, clientID)
	return SecretMatched, nil
}

func (v *countingVerifier) called() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.calls...)
}

// TestSecretComparisonOutsideAClientProofIsRefusedWithoutThePort — сверка,
// о которой движок просит вне доказательства клиента (в операции не спросили
// справочник о том, кто доказывает себя), отвергается случаем
// CodeCeremonyMisuse и порта не зовёт: сверять не с кем, а «совпал» о
// неназванном клиенте было бы доказательством ни о ком.
//
// Близнец — та же сверка после того, как справочник назвал клиента: порт
// позван один раз, о нём, и сверка прошла.
func TestSecretComparisonOutsideAClientProofIsRefusedWithoutThePort(t *testing.T) {
	verifier := &countingVerifier{}
	bridge := &storageBridge{ports: Ports{ClientSecrets: verifier}, timeout: time.Second}
	hasher := clientSecretHasher{bridge: bridge}

	ctx, notes := withNotes(context.Background())
	err := hasher.Compare(ctx, nil, []byte("presented"))
	if !errors.Is(err, ErrCeremonyMisuse) {
		t.Fatalf("сверка вне доказательства клиента: случай %v, ожидался %v: %v", CodeOf(err), CodeCeremonyMisuse, err)
	}
	if calls := verifier.called(); len(calls) != 0 {
		t.Fatalf("сверка вне доказательства клиента позвала порт %d раз: %v", len(calls), calls)
	}
	if recorded := notes.preferRecorded(nil); !errors.Is(recorded, ErrCeremonyMisuse) {
		t.Errorf("отказ не записан в ведомость операции: движок огрубил бы его, записано %v", recorded)
	}

	twinCtx, twinNotes := withNotes(context.Background())
	twinNotes.expectClientProof()
	twinNotes.noteClientClaim("svc-console", true)
	if err := hasher.Compare(twinCtx, nil, []byte("presented")); err != nil {
		t.Fatalf("близнец: сверка названного клиента отказала: %v", err)
	}
	if calls := verifier.called(); len(calls) != 1 || calls[0] != "svc-console" {
		t.Fatalf("близнец: порт позван %v, ожидался один вызов о svc-console", calls)
	}
}

// TestCeremonySecretHasherMintsNoHash — проверочное значение секрета чеканит
// служба, и хешер церемонии его не чеканит: движок на путях церемонии Hash не
// зовёт, а позови — получил бы отказ, а не значение, которое стало бы вторым
// проверочным значением мимо службы.
func TestCeremonySecretHasherMintsNoHash(t *testing.T) {
	hash, err := clientSecretHasher{}.Hash(context.Background(), []byte("presented"))
	if !errors.Is(err, ErrCeremonyMisuse) {
		t.Fatalf("Hash: случай %v, ожидался %v: %v", CodeOf(err), CodeCeremonyMisuse, err)
	}
	if hash != nil {
		t.Fatalf("Hash вернул значение %x", hash)
	}
}
