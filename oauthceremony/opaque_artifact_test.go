// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// opaque_artifact_test.go — код авторизации и токен обновления непрозрачны.
//
// Значение артефакта — 256 случайных бит, и ничего больше: ни подписи, ни
// общего секрета церемонии. В хранилище лежит не значение, а его подпись —
// `sha256` значения в hex, — поэтому украденная строка хранилища не
// предъявляется, а сверка идёт только поиском по подписи.
package oauthceremony_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// opaqueDigest — подпись непрозрачного артефакта, пересчитанная пробой от
// выданного значения, а не взятая у церемонии.
func opaqueDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// requireOpaque утверждает форму значения: 32 случайных байта в base64url без
// дополнения, и больше в значении нет ничего.
func requireOpaque(t *testing.T, what, value string) {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Errorf("%s %q — не base64url без дополнения: %v", what, value, err)
		return
	}
	if len(raw) != 32 {
		t.Errorf("%s: случайная часть %d байт, ожидалось 32 (256 бит)", what, len(raw))
	}
}

// TestOpaqueArtifactsAreRandomAndStoredUnderTheirDigest — предикат предмета
// кода и токена обновления: форма значения, подпись в хранилище, пересчитанная
// от выданного значения, и два выпуска подряд — два разных значения.
func TestOpaqueArtifactsAreRandomAndStoredUnderTheirDigest(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	firstCode, _ := issueCode(t, ceremony)
	secondCode, _ := issueCode(t, ceremony)
	if firstCode == secondCode {
		t.Fatalf("два выпуска кода подряд дали одно значение %q", firstCode)
	}
	for name, code := range map[string]string{"первый код": firstCode, "второй код": secondCode} {
		requireOpaque(t, name, code)
		store.mu.Lock()
		row, stored := store.codes[opaqueDigest(code)]
		var challenge, method string
		if stored {
			challenge, method = row.challenge, row.method
		}
		store.mu.Unlock()
		if !stored {
			t.Errorf("%s: запись кода лежит не под sha256 его значения", name)
			continue
		}
		// Привязка PKCE — поле той же записи (AuthorizationCodeRecord.ProofKey),
		// и под sha256 значения кода она лежит тем, что лежит там сама запись.
		if challenge == "" || method == "" {
			t.Errorf("%s: запись под sha256 его значения лежит без привязки PKCE", name)
		}
	}

	first, err := ceremony.Exchange(context.Background(), codeExchange(firstCode))
	if err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}
	second, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err != nil {
		t.Fatalf("оборот отказал: %v", err)
	}
	if first.RefreshToken == second.RefreshToken {
		t.Fatalf("два выпуска токена обновления подряд дали одно значение %q", first.RefreshToken)
	}

	issuances := store.issuer.issuances()
	if len(issuances) != 2 {
		t.Fatalf("у порта выпуска %d выпусков токена доступа, ожидалось 2 — токен доступа выпустил не порт", len(issuances))
	}
	for i, refresh := range []string{first.RefreshToken, second.RefreshToken} {
		what := []string{"первый токен обновления", "токен обновления оборота"}[i]
		requireOpaque(t, what, refresh)
		store.mu.Lock()
		row, stored := store.refresh[opaqueDigest(refresh)]
		store.mu.Unlock()
		if !stored {
			t.Errorf("%s: запись лежит не под sha256 его значения", what)
			continue
		}
		if want := issuances[i].issued.ID; row.accessSig != want {
			t.Errorf("%s связан с подписью токена доступа %q, а выпущен вместе с %q", what, row.accessSig, want)
		}
	}

	// Хранилище не держит ни одного значения: всякий ключ — подпись, и ни одна
	// подпись не совпадает с выданным значением.
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, value := range []string{firstCode, secondCode, first.RefreshToken, second.RefreshToken} {
		if _, leaked := store.codes[value]; leaked {
			t.Errorf("значение %q лежит в хранилище кодов как есть", value)
		}
		if _, leaked := store.refresh[value]; leaked {
			t.Errorf("значение %q лежит в хранилище токенов обновления как есть", value)
		}
	}
}
