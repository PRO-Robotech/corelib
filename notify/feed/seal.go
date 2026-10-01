// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// Отказы открытия секрета (З11). Оба — состояние кольца, а не значение строки:
// сервер ленты закрывает такую строку INVALID с причиной своего отказа.
var (
	// ErrKeyUnavailable — идентификатора ключа шифротекста нет в кольце
	// (причина key_unavailable, NTF1-B07). Перебора ключей нет (CX1-19).
	ErrKeyUnavailable = errors.New("feed: sealing key is not in the keyring")
	// ErrSealedMismatch — шифротекст не открывается ключом, который он называет:
	// перенесён в чужую строку, под чужой шаблон или повреждён (причина
	// sealed_mismatch, NTF1-B04, УК19).
	ErrSealedMismatch = errors.New("feed: sealed attributes do not open")
)

// KeySize — длина ключа AES-256-GCM.
const KeySize = 32

// Key — ключ кольца: однобайтовый идентификатор, который несёт шифротекст, и
// материал AES-256.
type Key struct {
	ID     byte
	Secret []byte
}

// Keyring — кольцо ключей ленты {активный, прежний} (З11). Запечатывает
// активным; открывает ключом, который назвал шифротекст. Реализует Sealer.
type Keyring struct {
	active   byte
	previous *byte
	aeads    map[byte]cipher.AEAD
}

// NewKeyring собирает кольцо: активный ключ и не больше одного прежнего с
// другим идентификатором; каждый — KeySize байт.
func NewKeyring(active Key, previous ...Key) (*Keyring, error) {
	if len(previous) > 1 {
		return nil, fmt.Errorf("feed: кольцо — активный и не больше одного прежнего ключа, получено прежних %d", len(previous))
	}
	r := &Keyring{active: active.ID, aeads: map[byte]cipher.AEAD{}}
	keys := append([]Key{active}, previous...)
	for i, k := range keys {
		if len(k.Secret) != KeySize {
			return nil, fmt.Errorf("feed: ключ %d кольца — %d байт, ожидается %d", k.ID, len(k.Secret), KeySize)
		}
		if _, dup := r.aeads[k.ID]; dup {
			return nil, fmt.Errorf("feed: идентификатор ключа %d в кольце дважды", k.ID)
		}
		block, err := aes.NewCipher(k.Secret)
		if err != nil {
			return nil, fmt.Errorf("feed: ключ %d кольца: %w", k.ID, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("feed: ключ %d кольца: %w", k.ID, err)
		}
		r.aeads[k.ID] = aead
		if i == 1 {
			id := k.ID
			r.previous = &id
		}
	}
	return r, nil
}

// keyringFile — форма файла секрета кольца: base64 стандартной азбуки, поле
// previous необязательно, неизвестные поля — отказ.
type keyringFile struct {
	Active   *keyringEntry `json:"active"`
	Previous *keyringEntry `json:"previous"`
}

type keyringEntry struct {
	ID  *uint8 `json:"id"`
	Key string `json:"key"`
}

func (e *keyringEntry) key(role string) (Key, error) {
	if e.ID == nil {
		return Key{}, fmt.Errorf("ключ %s без поля id", role)
	}
	secret, err := base64.StdEncoding.DecodeString(e.Key)
	if err != nil {
		return Key{}, fmt.Errorf("ключ %s: поле key не base64", role)
	}
	return Key{ID: *e.ID, Secret: secret}, nil
}

// ParseKeyring разбирает содержимое файла секрета кольца:
// {"active":{"id":N,"key":"<base64>"},"previous":{…}}. Текст отказа материала
// ключа не несёт.
func ParseKeyring(data []byte) (*Keyring, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f keyringFile
	if err := dec.Decode(&f); err != nil {
		return nil, errors.New("feed: файл кольца не разбирается — ожидается {\"active\":{\"id\":N,\"key\":\"<base64>\"},\"previous\":{…}}")
	}
	if f.Active == nil {
		return nil, errors.New("feed: в файле кольца нет активного ключа")
	}
	active, err := f.Active.key("active")
	if err != nil {
		return nil, fmt.Errorf("feed: файл кольца: %w", err)
	}
	var prev []Key
	if f.Previous != nil {
		p, err := f.Previous.key("previous")
		if err != nil {
			return nil, fmt.Errorf("feed: файл кольца: %w", err)
		}
		prev = append(prev, p)
	}
	return NewKeyring(active, prev...)
}

// LoadKeyring читает ручку knob (KACHO_<SVC>_NOTIFICATIONS_FEED_KEYRING —
// путь к файлу секрета) и разбирает кольцо (NTF1-B05). Незаданная и пустая
// ручка, нечитаемый файл и файл не по форме — отказ с именем ручки. Корень
// зовёт её при включённом флаге; окружение и файловую систему подаёт сам.
func LoadKeyring(knob string, lookup func(string) (string, bool), readFile func(string) ([]byte, error)) (*Keyring, error) {
	path, ok := lookup(knob)
	if !ok || path == "" {
		return nil, fmt.Errorf("feed: %s не задана — ключ ленты обязателен при включённой доставке", knob)
	}
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("feed: %s: файл кольца не читается: %w", knob, err)
	}
	r, err := ParseKeyring(data)
	if err != nil {
		return nil, fmt.Errorf("feed: %s: %w", knob, err)
	}
	return r, nil
}

// Report — строка самоотчёта посадки об оси ключа: идентификаторы, не
// материал.
func (r *Keyring) Report() string {
	prev := "none"
	if r.previous != nil {
		prev = fmt.Sprint(*r.previous)
	}
	return fmt.Sprintf("feed-keyring active=%d previous=%s", r.active, prev)
}

// aad — дополнительные данные AEAD: служба (префикс таблицы ленты), id строки
// и шаблон, каждая часть с длиной в восемь байт (З11). Две раскладки одной
// склейки невыразимы: длина записывается без усечения.
func aad(service, id, template string) []byte {
	var b []byte
	for _, part := range []string{service, id, template} {
		b = binary.BigEndian.AppendUint64(b, uint64(len(part)))
		b = append(b, part...)
	}
	return b
}

// Seal запечатывает plaintext активным ключом: идентификатор ключа ‖ nonce ‖
// шифротекст с тегом.
func (r *Keyring) Seal(service, id, template string, plaintext []byte) ([]byte, error) {
	aead := r.aeads[r.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("feed: nonce: %w", err)
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+aead.Overhead())
	out = append(append(out, r.active), nonce...)
	return aead.Seal(out, nonce, plaintext, aad(service, id, template)), nil
}

// Open открывает шифротекст ключом, который он называет. Ключа нет в кольце —
// ErrKeyUnavailable; не открылся — ErrSealedMismatch.
func (r *Keyring) Open(service, id, template string, sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, ErrSealedMismatch
	}
	aead, ok := r.aeads[sealed[0]]
	if !ok {
		return nil, fmt.Errorf("%w: идентификатор %d", ErrKeyUnavailable, sealed[0])
	}
	if len(sealed) < 1+aead.NonceSize()+aead.Overhead() {
		return nil, ErrSealedMismatch
	}
	nonce := sealed[1 : 1+aead.NonceSize()]
	pt, err := aead.Open(nil, nonce, sealed[1+aead.NonceSize():], aad(service, id, template))
	if err != nil {
		return nil, ErrSealedMismatch
	}
	return pt, nil
}
