// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"strconv"
)

// ── Привязка кода к доказательству владения ключом ──────────────────────────

// Протокольные поля запроса авторизации, несущие привязку (RFC 7636 §4.3).
const (
	formCodeChallenge       = "code_challenge"
	formCodeChallengeMethod = "code_challenge_method"
)

// s256ChallengeLength — длина вызова S256: base64url без выравнивания от
// 32 байт свёртки SHA-256 (RFC 7636 §4.2). Другой длины у вызова S256 не
// бывает, и вызов другой длины не совпадёт ни с одним доказательством.
const s256ChallengeLength = 43

// proofKeyBindingOf снимает привязку с протокольных полей запроса авторизации.
//
// Отказ — случай CodeInvalidRequest (RFC 7636 §4.4.1): запрос кода без годной
// привязки S256 не выпускает кода. Каждое поле обязано прийти РОВНО ОДИН раз
// (RFC 6749 §3.1): из двух вызовов движок взял бы первый, хранилище — какой
// придётся, и сверка шла бы не с тем, что прислал клиент.
func proofKeyBindingOf(form map[string][]string) (ProofKeyBinding, *ProtocolError) {
	challenges, methods := form[formCodeChallenge], form[formCodeChallengeMethod]
	if len(challenges) > 1 || len(methods) > 1 {
		return ProofKeyBinding{}, proofKeyRefusal("code_challenge was sent " + strconv.Itoa(len(challenges)) +
			" times and code_challenge_method " + strconv.Itoa(len(methods)) + " times; each goes exactly once")
	}
	var binding ProofKeyBinding
	if len(challenges) == 1 {
		binding.Challenge = challenges[0]
	}
	if len(methods) == 1 {
		binding.Method = ProofKeyMethod(methods[0])
	}
	if defect := bindingDefect(binding); defect != "" {
		return ProofKeyBinding{}, proofKeyRefusal(defect)
	}
	return binding, nil
}

// proofKeyRefusal — отказ запросу кода без годной привязки.
func proofKeyRefusal(defect string) *ProtocolError {
	return failf(CodeInvalidRequest, nil,
		"The authorization request carries no usable proof key.",
		"Send code_challenge=BASE64URL(SHA256(code_verifier)) with code_challenge_method=S256 (RFC 7636).",
		"proof key: "+defect)
}

// bindingDefect называет, чем привязка негодна; пусто — годна.
//
// Один судья на оба входа: запрос авторизации (отказ клиенту) и запись кода из
// хранилища (нарушение контракта порта). Разойтись им было бы негде.
func bindingDefect(b ProofKeyBinding) string {
	switch {
	case b.Challenge == "":
		return "code_challenge is missing"
	case !b.Method.Declared():
		return "code_challenge_method " + strconv.Quote(string(b.Method)) + " is not S256"
	case !isS256Challenge(b.Challenge):
		return "code_challenge is not a SHA-256 digest in base64url: 43 characters of [A-Za-z0-9_-]"
	}
	return ""
}

// isS256Challenge — вызов имеет форму свёртки S256: ровно 43 знака алфавита
// base64url без выравнивания.
func isS256Challenge(challenge string) bool {
	if len(challenge) != s256ChallengeLength {
		return false
	}
	for i := range len(challenge) {
		switch c := challenge[i]; {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
