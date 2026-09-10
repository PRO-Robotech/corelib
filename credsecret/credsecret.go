// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package credsecret — ЕДИНСТВЕННОЕ объявленное место, где живёт форма базового
// удостоверения Kachō: чеканка строки, её разбор, вычисление и сверка
// контрольной суммы, вычисление хранимого хеша.
//
// Приёмка BAT-1 §3.4 требует ровно одного объявления: чеканка в iam, полоса
// приёма на крае, полоса докера и гейт утёкшего креда зовут ЭТО место. Второй
// копии предиката не заводится ни в каком виде — ни своей регуляркой в гейте,
// ни разбором текста чужого исходника: копия разошлась бы молча и разошлась бы
// в сторону «принимаем больше», потому что расширять проще.
//
// Форма (§3.1):
//
//	kacho_<id удостоверения>_<26 знаков крокфорда><6 знаков контрольной суммы>
//
// Марка даёт сканеру утёкших кредов якорь и отличает наш вид ДО любого разбора;
// контрольная сумма отвергает опечатанный и обрезанный вход БЕЗ обращения к базе
// и БЕЗ обращения к авторитету (уровень 2 из трёх, §2.2).
package credsecret

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	// Mark — марка продукта. Якорь сканера утёкших кредов; по ней и только по
	// ней выбирается полоса приёма (§5.1) — никогда по неудаче разбора другого
	// вида.
	Mark = "kacho_"

	// SecretPartLen — 26 знаков крокфорда = 130 бит из криптографического
	// источника. Энтропия — довод §2.1 (перебор невозможен арифметически,
	// поэтому медленной функции вывода ключа не вводится), а не украшение.
	SecretPartLen = 26

	// ChecksumLen — 6 знаков крокфорда = 30 бит.
	ChecksumLen = 6

	// tailLen — секретная часть вместе с контрольной суммой.
	tailLen = SecretPartLen + ChecksumLen

	// sep — разделитель между идентификатором и хвостом. Идентификатор в этом
	// дереве бывает формы `<prefix>_<body>`, то есть САМ содержит разделитель,
	// поэтому разбор идёт по ПОСЛЕДНЕМУ вхождению (§3.2).
	sep = '_'

	// alphabet — крокфордов base32 в нижнем регистре: нет знаков, путающихся
	// при чтении вслух и при переписывании; нет `+`, `/`, `=`, ломающих строку
	// в URL и в оболочке.
	alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

	// hashDomain и checksumDomain разделяют два вывода из одного материала.
	// Разделение доменов обязательно: без него 30 бит контрольной суммы,
	// которые предъявитель несёт открыто, были бы префиксом ХРАНИМОГО хеша.
	hashDomain     = "kacho.credential.secret.v1\x00"
	checksumDomain = "kacho.credential.checksum.v1\x00"
)

// ErrNotOurLane — строка не несёт нашей марки. Отдельный от ErrMalformed исход:
// «это не наша полоса» и «наша полоса, вход негоден» — разные вещи для
// классификатора приёма (§5.1), и слить их значило бы завести запасной путь,
// срабатывающий на неудаче.
var ErrNotOurLane = errors.New("credsecret: строка не несёт марки базового удостоверения")

// ErrMalformed — марка наша, форма негодна: длина, алфавит, разделитель или
// контрольная сумма. Наружу этот исход не различается (§10) — различимость
// живёт внутрь, в счётчиках.
var ErrMalformed = errors.New("credsecret: негодная форма базового удостоверения")

// Parsed — разобранная строка. Идентификатор дословно равен первичному ключу
// строки реестра: второго имени у удостоверения нет (§3.3).
type Parsed struct {
	Mark         string
	CredentialID string
	SecretPart   string
	Checksum     string
}

// HasMark отвечает, наша ли это полоса. Вызывается ПЕРВЫМ на пути приёма и
// ничего, кроме префикса, не смотрит: уровень 1 отсева (§2.2) стоит на пути
// каждого запроса и не вправе платить больше сравнения префикса.
func HasMark(s string) bool { return strings.HasPrefix(s, Mark) }

// Parse разбирает и проверяет форму, включая контрольную сумму. Обращения ни к
// базе, ни к авторитету не делает — это уровень 2 отсева.
func Parse(s string) (Parsed, error) {
	if !HasMark(s) {
		return Parsed{}, ErrNotOurLane
	}
	rest := s[len(Mark):]

	// По ПОСЛЕДНЕМУ разделителю: идентификатор сам может его содержать (§3.2).
	i := strings.LastIndexByte(rest, sep)
	if i <= 0 {
		return Parsed{}, fmt.Errorf("%w: разделитель не найден", ErrMalformed)
	}
	credID, tail := rest[:i], rest[i+1:]
	if len(tail) != tailLen {
		return Parsed{}, fmt.Errorf("%w: хвост %d знаков, объявлено %d", ErrMalformed, len(tail), tailLen)
	}
	if !isAlphabet(tail) {
		return Parsed{}, fmt.Errorf("%w: знак вне объявленного алфавита", ErrMalformed)
	}

	p := Parsed{
		Mark:         Mark,
		CredentialID: credID,
		SecretPart:   tail[:SecretPartLen],
		Checksum:     tail[SecretPartLen:],
	}
	// Контрольная сумма покрывает идентификатор ВМЕСТЕ с секретной частью,
	// поэтому половина одного удостоверения, приставленная к половине другого,
	// отвергается здесь же — без базы и без авторитета (BAT-1-06).
	if subtle.ConstantTimeCompare([]byte(p.Checksum), []byte(checksum(credID, p.SecretPart))) != 1 {
		return Parsed{}, fmt.Errorf("%w: контрольная сумма не сходится", ErrMalformed)
	}
	return p, nil
}

// Mint чеканит строку для названного удостоверения и возвращает вместе с ней
// хеш, который единственно и хранится. Сам секрет не хранится нигде (§4.3).
func Mint(credentialID string) (secret string, hash []byte, err error) {
	return MintFrom(rand.Reader, credentialID)
}

// MintFrom — Mint с названным источником случайности. Существует ради пробы
// BAT-1-08: сорванный источник обязан дать ОТКАЗ, а не строку предсказуемого
// вида. Прод-путь зовёт Mint.
func MintFrom(src io.Reader, credentialID string) (secret string, hash []byte, err error) {
	if credentialID == "" {
		return "", nil, errors.New("credsecret: идентификатор удостоверения пуст")
	}
	if strings.ContainsAny(credentialID, " \t\n") {
		return "", nil, errors.New("credsecret: идентификатор удостоверения несёт пробельный знак")
	}
	part, err := randomPart(src)
	if err != nil {
		// Отказ, а не запасной путь: строка предсказуемого вида здесь была бы
		// удостоверением, которое можно угадать.
		return "", nil, fmt.Errorf("credsecret: чеканка отказана, источник случайности недоступен: %w", err)
	}
	return Mark + credentialID + string(sep) + part + checksum(credentialID, part), Hash(credentialID, part), nil
}

// Hash — то, что хранится: sha256 по идентификатору И секретной части вместе
// (§2.1). Половина одного удостоверения с половиной другого хешу не подходит —
// это следует из формы хеша, а не из проверки.
func Hash(credentialID, secretPart string) []byte {
	sum := sha256.Sum256([]byte(hashDomain + credentialID + "\x00" + secretPart))
	return sum[:]
}

// Verify сверяет предъявленную секретную часть с хранимым хешем за постоянное
// время.
func Verify(credentialID, secretPart string, stored []byte) bool {
	if len(stored) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(Hash(credentialID, secretPart), stored) == 1
}

// patternRe — объявленный предикат формы. Объявлен ЗДЕСЬ и только здесь; гейт
// утёкшего креда зовёт Pattern(), а не пишет свою регулярку.
var patternRe = regexp.MustCompile(
	`\bkacho_[0-9a-z]{2,}(?:_[0-9a-z]+)?_[0-9a-hjkmnp-tv-z]{` +
		itoa(tailLen) + `}\b`,
)

// Pattern возвращает предикат формы для сканера утёкших кредов. Ровно одно
// объявление на дерево (§3.4, BAT-1-65).
func Pattern() *regexp.Regexp { return patternRe }

func isAlphabet(s string) bool {
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// randomPart берёт SecretPartLen знаков алфавита из криптографического
// источника. Алфавит ровно 32 знака, поэтому пятибитная нарезка равномерна
// by construction — модульного смещения нет и отбрасывать нечего.
func randomPart(src io.Reader) (string, error) {
	// 26 знаков × 5 бит = 130 бит ⇒ 17 байт (136 бит) с запасом.
	const nbytes = (SecretPartLen*5 + 7) / 8
	buf := make([]byte, nbytes)
	if _, err := io.ReadFull(src, buf); err != nil {
		return "", err
	}
	out := make([]byte, SecretPartLen)
	var acc uint32
	var bits uint
	bi := 0
	for i := 0; i < SecretPartLen; i++ {
		for bits < 5 {
			acc = acc<<8 | uint32(buf[bi])
			bi++
			bits += 8
		}
		bits -= 5
		out[i] = alphabet[(acc>>bits)&0x1f]
	}
	return string(out), nil
}

// checksum — 30 бит от идентификатора и секретной части, в том же алфавите.
// Домен отделён от домена хранимого хеша: иначе открыто предъявляемая
// контрольная сумма была бы префиксом того, что лежит в базе.
func checksum(credentialID, secretPart string) string {
	sum := sha256.Sum256([]byte(checksumDomain + credentialID + "\x00" + secretPart))
	v := binary.BigEndian.Uint32(sum[:4]) >> 2 // 30 бит
	out := make([]byte, ChecksumLen)
	for i := ChecksumLen - 1; i >= 0; i-- {
		out[i] = alphabet[v&0x1f]
		v >>= 5
	}
	return string(out)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
