// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/pagetoken"
)

// drain вычитывает окно `(cursor, settled]` до конца и отдаёт то, что вызывающий
// вправе видеть. Возвращает новый курсор.
//
// # Почему окно, а не просто «больше курсора»
//
// Номер выдаётся счётчиком на вставке, а строка становится видимой на фиксации,
// поэтому порядок номеров и порядок фиксаций НЕЗАВИСИМЫ. Отдать номер, за
// которым ещё может появиться меньший, — значит потерять меньший навсегда:
// перечитывание идёт строго «больше курсора», и возобновление с выданной позиции
// воспроизводит ту же дыру. Граница устоявшегося (см. `watermark.go`)
// гарантирует обратное: к моменту отдачи события все меньшие номера уже
// определены — видимы либо потеряны откатом.
//
// # Курсор идёт по ПРОЧИТАННОЙ строке, а не по отправленной
//
// Батч читается целиком, затем сужается, затем отправляется. Позиция
// продвигается до последней ПРОЧИТАННОЙ строки: шла бы она за отправленными,
// полный батч невидимых вызывающему строк перечитывался бы вечно — тот же
// запрос, те же сто строк, ноль продвижения, и всё это внутри занятого слота.
// То же правило, по которому страничные списки выводят следующий курсор из
// последней ПРОСМОТРЕННОЙ строки.
//
// # ИСКЛЮЧЕНИЕ ровно одно: ОКНО МАТЕРИАЛИЗАЦИИ ГРАНТА
//
// Пообъектное «нет» бывает ВРЕМЕННЫМ: кортеж владения кладётся ПОСЛЕ фиксации
// ресурсной строки, а пробуждение приходит НА фиксации. Правило выше делало
// такое «нет» окончательным — строка уезжала под курсор, и открытый поток не
// отдавал её больше никогда, тихо (задача продукта #2264). Поэтому курсор НЕ
// переходит первую такую строку, пока её окно не истрачено; окно и его цена —
// `grant.go`, и здесь они не пересказываются.
//
// Вечного перечитывания это не возвращает: окно конечно, по его истечении
// строка снимается, а расхождение «прочитано против отдано» называется
// переписью ([delivery.census]).
func (s *Server) drain(
	ctx context.Context,
	conn *pgx.Conn,
	h *Watermark,
	d *delivery,
	cursor int64,
	filter Filter,
	stream subscriptionv1.InternalSubscriptionService_SubscribeServer,
) (int64, error) {
	for {
		if ctx.Err() != nil {
			return cursor, nil
		}
		settled := h.Settled()
		if settled <= cursor {
			return cursor, nil
		}

		rows, err := s.read(ctx, conn, cursor, settled, filter)
		if err != nil {
			if ctx.Err() != nil {
				return cursor, nil
			}
			// Отказ чтения — fail-closed. Недоступность источника НЕ означает
			// «событий нет»: пустая выдача была бы утверждением о мире.
			return cursor, status.Error(codes.Unavailable, "subscription backend unavailable")
		}

		// НИЖНЯЯ ГРАНИЦА ПЕРЕСПРАШИВАЕТСЯ ПЕРЕД КАЖДОЙ ПАРТИЕЙ — у владельца,
		// который журнал ЧИСТИТ.
		//
		// Она объявляется подписчику ОДИН раз, при открытии, и этого довольно
		// ровно до тех пор, пока журнал не убирают под работающим потоком.
		// Уборка, пришедшая между двумя партиями, снимает строки из окна
		// `(курсор, устоявшееся]` — выборка их просто не находит, курсор
		// переезжает через них по последней прочитанной позиции, и подписчик
		// получает НЕПОЛНОЕ, ничем не отличимое от «изменений не было». Это тот
		// же исход, ради явности которого заведён `positionLost` на открытии, —
		// значит и здесь он обязан быть отказом, а не тишиной.
		//
		// Инвариант, которым это закрывается: `курсор >= пол` ⟹ выше курсора
		// снятого нет. Пол есть «самая ранняя удержанная минус один», поэтому всё
		// строго выше курсора лежит в удерживаемом участке by construction, — а
		// снимается участок ПРЕФИКСОМ (см. `journalSweepSQL`, решение 1), поэтому
		// дыр над полом не бывает.
		//
		// ПОЧЕМУ ПОСЛЕ СТРАНИЦЫ, А НЕ ДО НЕЁ — порядок здесь НЕСУЩИЙ
		//
		// Пол и страница суть ДВА запроса, и уборка вправе зафиксироваться между
		// ними. Спрошенный РАНЬШЕ, пол описывает журнал, которого к моменту
		// выборки уже нет: страница приходит без снятых строк, пол о них ещё не
		// знает, отказа не будет — и подписчик получает страницу с дырой как
		// полную. Решает тогда не проверка, а расписание, то есть это
		// software check-then-act (ban #10) в чистом виде.
		//
		// Порядок «после» делает вывод ДОКАЗУЕМЫМ. Нижняя строка непустого
		// журнала монотонна (снимается префикс, номера растут), поэтому
		// наблюдение, взятое в снимке не раньше страницы, занизиться не может:
		// пропала строка из окна ⟹ пол выше курсора ⟹ отказ. Обратный размен
		// назван честно и он в пользу подписчика: уборка, поспевшая ПОСЛЕ
		// выборки, отвергает страницу, которая уже была в руках, — исход
		// пессимистичный, но никогда не молчаливый.
		//
		// ПОЧЕМУ ПОСЛЕ ПРОВЕРКИ «НЕЧЕГО ОТДАВАТЬ», А НЕ ДО НЕЁ
		//
		// Предмет стража — потеря в окне `(курсор, устоявшееся]`. При
		// `устоявшееся <= курсор` окно ПУСТО, терять нечего, и вопрос был бы
		// задан впустую. Разница не косметическая: холостой перепрос идёт раз в
		// две секунды на КАЖДЫЙ открытый поток, и страж до проверки означал бы
		// столько же запросов в секунду на молчащих подписчиках — плата за
		// наблюдение состояния, которое не может измениться. Ранний выход выше
		// эту цену снимает и после переноса: пол по-прежнему не спрашивается у
		// догнавшего потока.
		//
		// Догнавший поток при этом ничего не теряет by construction: строк выше
		// его курсора в пределах устоявшегося нет вовсе, а всё, что появится
		// позже, моложе окна удержания и под уборку не подпадает. Отстанет —
		// `устоявшееся` уйдёт вперёд, ранний выход перестанет срабатывать, и
		// страж спросит границу на первой же партии.
		//
		// ПУСТАЯ СТРАНИЦА ПРОХОДИТ ЧЕРЕЗ ЭТУ ЖЕ ПРОВЕРКУ, и это не деталь
		// порядка строк: окно, вычищенное ЦЕЛИКОМ, выборка возвращает пустым, а
		// ниже пустая страница двигает курсор на границу устоявшегося. Стой
		// проверка после неё — снятый участок уезжал бы под курсор молча, то
		// есть ровно тем исходом, ради которого пол и заведён.
		//
		// Наблюдатель у потока СВОЙ (см. [Server.serve]), и вопрос с ответом
		// разделены здесь только потому: единственная горутина потока стоит
		// между ними, и вернуть поле назад чужим старым наблюдением некому.
		// Станет наблюдатель общим — берите [Watermark.ObserveFloor], который
		// отвечает из своего наблюдения и заведён ровно под этот случай.
		//
		// Цену платит ТОЛЬКО чистящий владелец: у объявившего [RetainsEverything]
		// нижней границы не существует, вопрос не задаётся, и стоимость партии не
		// меняется ни на один запрос.
		if s.cfg.Journal.Storage.Retention == RetainsFromEarliestRow {
			if err := h.RefreshEarliest(ctx, conn); err != nil {
				if ctx.Err() != nil {
					return cursor, nil
				}
				return cursor, status.Error(codes.Unavailable, "subscription backend unavailable")
			}
			if floor := h.Floor(RetainsFromEarliestRow); cursor < floor {
				return cursor, positionLost(pagetoken.EncodeSubscriptionPosition(
					pagetoken.SubscriptionPosition{Settled: floor}))
			}
		}

		if len(rows) == 0 {
			return settled, nil
		}

		// Право на ПРОЕКТ спрашивается заново — перед КАЖДОЙ порцией, которая
		// собирается уйти. См. [Server.regate].
		if err := s.regate(ctx, filter); err != nil {
			return cursor, err
		}

		scanned := rows[len(rows)-1].Position
		d.read(rows)

		events, err := s.mapRows(rows, filter)
		if err != nil {
			return cursor, err
		}
		verdicts, err := s.narrow(ctx, rows, events, filter)
		if err != nil {
			// Модель не ответила — это НЕ «да». Позиция не двигается, строки не
			// уходят.
			return cursor, err
		}

		// ОТСРОЧКА СУЖДЕНИЯ О ВИДИМОСТИ — окно материализации гранта (`grant.go`).
		//
		// Кортеж владения кладётся ПОСЛЕ фиксации ресурсной строки, а пробуждение
		// приходит НА фиксации: пообъектное «нет», полученное в этот миг,
		// законно и при этом ВРЕМЕННО. Курсор идёт по прочитанной строке, поэтому
		// без отсрочки такая строка уезжает под него и открытый поток не отдаёт
		// её больше никогда — тихо, без пропуска в нумерации и без отказа.
		//
		// Отсрочка держит партию С ПЕРВОЙ такой строки: отдать то, что за ней, и
		// вернуться к ней позже нельзя — позиция едет клиенту заголовком
		// возобновления, и меньшая после большей отбросила бы его назад.
		//
		// Она НЕ расширяет видимость: наружу не уходит ни одна строка, которой
		// модель не сказала «да». Истратив окно, строка снимается и попадает в
		// перепись — расхождение «прочитано против отдано» остаётся видимым.
		reachesHead := len(rows) < readBatch || scanned >= settled
		if first := firstWithheld(verdicts); first >= 0 {
			held := rows[first].Position - 1
			d.released(held)
			if d.holds(held, reachesHead, s.now()) {
				if err := s.sendVisible(events, verdicts[:first], d, stream); err != nil {
					return cursor, err
				}
				return held, nil
			}
			d.rowsWithheld += int64(countWithheld(verdicts))
		}

		if err := s.sendVisible(events, verdicts, d, stream); err != nil {
			return cursor, err
		}
		cursor = scanned
		d.released(cursor)
		d.atHead = reachesHead

		if len(rows) < readBatch {
			return cursor, nil
		}
	}
}

// sendVisible отправляет события, которым модель сказала «да», СОХРАНЯЯ порядок.
//
// Длина `verdicts` задаёт ГРАНИЦУ отправки: удержанная партия отдаёт только
// префикс до первой невидимой строки, полная — весь батч. Граница передаётся
// срезом вердиктов, а не индексом, потому что срез нельзя перепутать местами с
// длиной батча.
func (s *Server) sendVisible(
	events []*subscriptionv1.SubscriptionEvent,
	verdicts []rowVerdict,
	d *delivery,
	stream subscriptionv1.InternalSubscriptionService_SubscribeServer,
) error {
	for i, v := range verdicts {
		if v != rowDeliver {
			continue
		}
		if err := stream.Send(&subscriptionv1.SubscriptionMessage{
			Message: &subscriptionv1.SubscriptionMessage_Event{Event: events[i]},
		}); err != nil {
			return err
		}
		d.eventsSent++
	}
	return nil
}

// firstWithheld — индекс первой строки, чьё «нет» может ещё стать «да»
// (-1, если такой нет).
//
// Ищется ИМЕННО пообъектный отказ. Строка недоставляемая ([rowDrop]) не станет
// доставляемой ни через какое время: у неё нет типа объекта, рода изменения либо
// якоря, а отказ по якорю относится к ЧУЖОМУ проекту, доступ к которому этим
// событием не создаётся. Держать поток на них значило бы платить окном за то,
// что не изменится.
func firstWithheld(verdicts []rowVerdict) int {
	for i, v := range verdicts {
		if v == rowWithhold {
			return i
		}
	}
	return -1
}

// countWithheld — сколько строк партии снимается по истечении окна. Считается
// вся партия, а не одна первая: окно они простояли вместе, потому что каждый
// проход перечитывает ТО ЖЕ окно и спрашивает о них заново.
func countWithheld(verdicts []rowVerdict) int {
	n := 0
	for _, v := range verdicts {
		if v == rowWithhold {
			n++
		}
	}
	return n
}

// regate переспрашивает стража проектной оси ПЕРЕД отправкой порции.
//
// # Почему на живом потоке, а не только при открытии
//
// Страж спрашивался один раз — при открытии, — и дальше поток жил сам. Это
// ровно тот класс, который корпус называет «контроль, действующий на выдаче, но
// не на предъявлении»: механизм присутствует, выглядит исполненным и не
// срабатывает никогда, потому что стоит не на том пути. От задержки
// распространения он отличается тем, что сходиться тут нечему: отзыв не
// приобретает читателя оттого, что прошло время.
//
// Пообъектное сужение ниже этой дыры НЕ закрывает, хотя выглядит так, будто
// закрывает, — и различие НЕ в том, где живёт истина: внешнего движка прав в
// дереве нет вовсе, обе стороны спрашивают одну живую таблицу. Различие в КЭШЕ:
// постраничный путь держит окно ПОЛОЖИТЕЛЬНЫХ вердиктов
// ([listnarrow.Config.CacheTTL]), поэтому строка, разрешённая до отзыва,
// продолжает уходить всё это окно. Страж оси окна не держит и отрицателен с
// первого же вопроса после отзыва.
//
// Окно это числом названо — там же, у сужателя, — но названо ОНО ЖЕ и для
// строк: то есть без переспроса стража отзыв доезжал бы до потока не раньше
// истечения окна вердиктов, а до СОЕДИНЕНИЯ не доезжал бы никогда.
//
// Отдельно и важно: страж оси спрашивает право ВЫЗЫВАЮЩЕГО на проект, и модель
// разрешает его членством в группе наравне с прямой выдачей. Поэтому отзыв
// привязки, субъектом которой была ГРУППА, эта проверка закрывает, хотя по
// имени такую строку закрыть нельзя — субъекта `group:…` в реестре потоков не
// бывает (см. `pkg/subjectchange`, счётчик `usersets_skipped` — он считает
// ИМЕННО эту норму и отделён от счётчика потерянных имён, kacho#1463).
//
// # Почему ПЕРЕД порцией, а не по таймеру
//
// Окно тогда есть СЛЕДСТВИЕ РЕШЕНИЯ, а не срока: после отзыва не уходит НИ ОДНОЙ
// порции, и величины, которую надо было бы выбрать и потом объяснять, здесь нет
// вовсе. Молчащий поток при этом не платит ничего — расход пропорционален
// объёму событий, а не числу открытых потоков.
//
// # Чего эта проверка НЕ делает
//
// Она не закрывает СОЕДИНЕНИЕ: поток остаётся открытым и перестаёт отдавать
// события. Закрытие соединения принадлежит тому, кто его открыл, — у
// сегодняшнего единственного потребителя это край (kacho#1022), и там оно
// сделано чтением отзыва. Сказано вслух, чтобы следующий читатель не принял
// «событий нет» за «потока нет».
//
// Fail-closed: неотвеченная модель НЕ есть «право есть» — отказ уходит наверх, и
// порция не отправляется.
func (s *Server) regate(ctx context.Context, filter Filter) error {
	if filter.ProjectID == "" {
		// Оси нет — сторожить нечего. Спрашивать здесь значило бы звать соседа с
		// вопросом, которого вызывающий не задавал.
		return nil
	}
	allowed, err := listnarrow.AllowedOnObject(ctx, s.cfg.Narrower,
		s.cfg.ProjectGate.ObjectType, s.cfg.ProjectGate.Action,
		s.cfg.ProjectGate.Relations, filter.ProjectID)
	if err != nil {
		return err
	}
	if !allowed {
		// Та же форма, что при открытии, и дословно та же, что у отсутствия
		// проекта: различимый текст выдал бы существование чужого проекта.
		return status.Errorf(codes.NotFound, s.cfg.ProjectGate.NotFoundFormat, filter.ProjectID)
	}
	return nil
}

// read вычитывает одну порцию окна, применяя те оси, которые отбираются запросом.
//
// Ось `project_id` уходит в запрос ТОЛЬКО у владельца, чей журнал несёт колонку;
// у остальных якорь даёт отображение, и ось отбирается после него — по-прежнему
// СЕРВЕРОМ (см. [Server.mapRows]).
func (s *Server) read(
	ctx context.Context,
	conn *pgx.Conn,
	cursor, settled int64,
	filter Filter,
) ([]Row, error) {
	st := s.cfg.Journal.Storage

	projectExpr := "''"
	if st.Project == ProjectInColumn {
		projectExpr = st.ProjectColumn
	}

	args := []any{cursor, settled}
	var where strings.Builder
	if len(filter.Kinds) > 0 {
		// Вид, названный вызывающим, — слово ПРОВОДА (тип объекта модели прав),
		// а колонка хранит слово ВЛАДЕЛЬЦА. Перевод живёт в объявлении журнала:
		// подставить сюда вид провода как есть значило бы отобрать по слову,
		// которого в колонке нет, — и подписка молчала бы навсегда, выглядя
		// исправной.
		args = append(args, s.cfg.Journal.journalWords(filter.Kinds))
		fmt.Fprintf(&where, " AND %s = ANY($%d)", st.KindColumn, len(args))
	}
	if len(filter.IDs) > 0 {
		args = append(args, filter.IDs)
		fmt.Fprintf(&where, " AND %s = ANY($%d)", st.IDColumn, len(args))
	}
	if filter.ProjectID != "" && st.Project == ProjectInColumn {
		args = append(args, filter.ProjectID)
		fmt.Fprintf(&where, " AND %s = $%d", st.ProjectColumn, len(args))
	}

	q := fmt.Sprintf(`
		SELECT %s, %s, %s, %s, %s, %s
		FROM %s
		WHERE %s > $1 AND %s <= $2%s
		ORDER BY %s ASC
		LIMIT %d`,
		st.PositionColumn, st.KindColumn, st.IDColumn, projectExpr, st.ChangeColumn, st.PayloadColumn,
		st.Table,
		st.PositionColumn, st.PositionColumn, where.String(),
		st.PositionColumn, readBatch)

	pgRows, err := conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer pgRows.Close()

	out := make([]Row, 0, readBatch)
	for pgRows.Next() {
		var r Row
		if err := pgRows.Scan(&r.Position, &r.Kind, &r.ID, &r.ProjectID, &r.Change, &r.Payload); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := pgRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// mapRows превращает строки в события общей формы, сохраняя ПОРЯДОК.
//
// Соответствие позиционное: `events[i]` отвечает `rows[i]`, а недоставляемая
// строка даёт `nil`. Отбрасывать её здесь нельзя — сужение по правам
// сопоставляет батч со строками по индексу, и сдвиг разъехался бы молча.
//
// # Три исхода, и каждый назван
//
//   - вид вне закрытого словаря владельца ЛИБО род изменения вне его словаря —
//     строка НЕДОСТАВЛЯЕМА: вопрос о её видимости задать нечем, а род изменения
//     подписчику не назвать. Громко в лог: это расхождение журнала владельца с
//     его же объявлением, а не свойство подписки;
//   - якорь не добылся — строка НЕДОСТАВЛЯЕМА по той же причине: решение о
//     показе принять не из чего. Fail-closed;
//   - состояние не собралось — событие ДОСТАВЛЯЕТСЯ с признаком «состояния не
//     будет» и названной причиной. Пустая нагрузка вместо состояния не
//     выразима формой by construction, и это тот случай, ради которого ветвление
//     носителя и заведено.
func (s *Server) mapRows(rows []Row, filter Filter) ([]*subscriptionv1.SubscriptionEvent, error) {
	m := s.cfg.Journal.Mapping
	out := make([]*subscriptionv1.SubscriptionEvent, len(rows))

	var undeliverable int
	for i, row := range rows {
		if _, ok := m.Kinds[row.Kind]; !ok {
			undeliverable++
			continue
		}
		change, ok := m.Changes[row.Change]
		if !ok {
			undeliverable++
			continue
		}

		project := row.ProjectID
		if m.Anchor != nil {
			anchored, err := m.Anchor(row)
			if err != nil {
				s.log.Error("subscription: row is undeliverable — the project anchor could not be derived",
					"position", row.Position, "kind", row.Kind, "err", err)
				undeliverable++
				continue
			}
			project = anchored
		}
		// Ось, отобранная не запросом, отбирается здесь — СЕРВЕРОМ. Клиенту
		// доотбор по якорной оси не передаётся ни при каком устройстве журнала.
		if filter.ProjectID != "" && project != filter.ProjectID {
			continue
		}

		ev := &subscriptionv1.SubscriptionEvent{
			Position: pagetoken.EncodeSubscriptionPosition(pagetoken.SubscriptionPosition{Settled: row.Position}),
			// На провод едет ТИП ОБЪЕКТА, а не слово, которым владелец записал
			// строку: написание вида одно на всё дерево, и берётся оно у того же
			// производителя, которым сервер спрашивает модель прав о видимости
			// этой самой строки. Отдать `row.Kind` значило бы завести третье
			// написание — которого нет ни в словаре открытия, ни в оси отбора.
			Kind:       m.Kinds[row.Kind].ObjectType,
			ResourceId: row.ID,
			ProjectId:  project,
			Change:     change,
		}
		state, absence, err := m.State(row)
		if complaint := setStateCarrier(ev, state, absence, err); complaint != "" {
			s.log.Warn("subscription: "+complaint,
				"position", row.Position, "kind", row.Kind, "change", row.Change, "err", err)
		}
		out[i] = ev
	}

	if undeliverable > 0 {
		s.log.Warn("subscription: rows dropped — the journal carries kinds or changes the owner did not declare",
			"dropped", undeliverable, "table", s.cfg.Journal.Storage.Table)
	}
	return out, nil
}

// setStateCarrier выбирает НОСИТЕЛЬ НАГРУЗКИ события и возвращает жалобу в
// журнал процесса («» — жаловаться не на что).
//
// Функция чистая и потому проверяема без базы, без сервера и без транспорта:
// предмет здесь — таблица из четырёх исходов, а не путь события, и проверять её
// поднятым стендом значило бы платить контейнером за утверждение об операторе
// `switch`.
//
// # ЧЕТЫРЕ ИСХОДА, И НИ ОДИН НЕ СВОДИТСЯ К ДРУГОМУ
//
//   - ОТКАЗ СБОРКИ — состояние есть, собрать не удалось. `NOT_SERIALIZABLE`, и
//     только здесь: слово означает неудавшуюся ПОПЫТКУ, и назвать им отсутствие
//     попытки значит соврать подписчику о роде беды. Действие у него разумное —
//     перечитать;
//   - СОСТОЯНИЕ СОБРАНО — оно и едет;
//   - ОТСУТСТВИЕ С НАЗВАННОЙ ПРИЧИНОЙ — едет причина владельца. Пустое состояние
//     не отдаётся НИКОГДА: подписчик вправе читать непустую нагрузку как ПОЛНОЕ
//     состояние предмета, и пустой объект солгал бы ему;
//   - ОТСУТСТВИЕ БЕЗ ПРИЧИНЫ — `REASON_UNSPECIFIED` и ГРОМКО. Контракт держит
//     это значение ровно для такого случая; подшить его к соседней записи
//     означало бы завести корзину «прочее» под чужим именем.
//
// # ПОЧЕМУ ОТКАЗ СИЛЬНЕЕ НАЗВАННОЙ ПРИЧИНЫ
//
// Владелец, вернувший и отказ, и причину, противоречит сам себе, и сервер обязан
// выбрать ту сторону, где потеря меньше. Отказ — наблюдение о СЛУЧИВШЕМСЯ, причина
// — объявление о ЗАДУМАННОМ; отдать причину значило бы объявить свойством журнала
// то, что на самом деле сломалось, и погасить единственный след поломки.
//
// # ПОЧЕМУ СОБРАННОЕ СОСТОЯНИЕ СИЛЬНЕЕ НАЗВАННОЙ ПРИЧИНЫ
//
// Причина при непустом состоянии — противоречие того же рода, и разрешается оно в
// пользу состояния: подписчику нужен предмет, а не объяснение, почему его нет.
// Жалоба при этом пишется — противоречие в объявлении владельца обязано быть
// видно, а не проглочено.
func setStateCarrier(
	ev *subscriptionv1.SubscriptionEvent,
	state *anypb.Any,
	absence StateAbsence,
	err error,
) (complaint string) {
	unavailable := func(r subscriptionv1.SubscriptionEvent_StateUnavailable_Reason) {
		ev.Carrier = &subscriptionv1.SubscriptionEvent_StateUnavailable_{
			StateUnavailable: &subscriptionv1.SubscriptionEvent_StateUnavailable{Reason: r},
		}
	}

	switch {
	case err != nil:
		unavailable(subscriptionv1.SubscriptionEvent_StateUnavailable_NOT_SERIALIZABLE)
		return "state could not be assembled for this event"
	case state != nil:
		ev.Carrier = &subscriptionv1.SubscriptionEvent_State{State: state}
		if absence != StateAbsenceUnnamed {
			return "the owner returned both a state and a reason for its absence — the state wins, but the declaration contradicts itself"
		}
		return ""
	default:
		word, named := absence.reason()
		unavailable(word)
		if !named {
			// Сюда попадают ОБА негодных исхода — забытая причина и значение вне
			// словаря, — и оба ГРОМКИЕ. Смолчать на втором значило бы вернуть
			// корзину «прочее» под именем нулевого значения: дефект объявления
			// владельца стал бы неотличим от законного исхода.
			return "the owner returned no state and named no reason — the subscriber cannot tell a journal property from a failure"
		}
		return ""
	}
}

// narrow выносит вердикт по КАЖДОЙ строке партии, ПОЗИЦИОННО: `out[i]` отвечает
// `rows[i]`.
//
// Отфильтрованный список здесь не годится, и это не стиль. Вызывающему нужно
// отличить «нет сейчас» от «нет никогда»: первое снимается материализацией
// гранта и потому подлежит отсрочке, второе — не изменится ни через какое время
// (см. [rowVerdict] и `grant.go`). Список видимых оба «нет» сводит в одно —
// отсутствие элемента, — и выбор между потерей события и вечным удержанием
// становится невыразим.
//
// Порядок СОХРАНЯЕТСЯ: журнал упорядочен по номеру, и перестановка сломала бы
// монотонность потока.
//
// Вопрос задаётся партиями не больше [visibilityBatch] и группируется по типу
// объекта: у модели спрашивают про однотипные идентификаторы. Ошибка любой
// партии прекращает обработку батча ЦЕЛИКОМ — частично сужённый батч отдавать
// нельзя.
//
// # СНЯТИЕ судится ЯКОРЕМ, а не предметом, и это не послабление
//
// У события снятия предмета уже нет — ни в базе владельца, ни в модели прав:
// путь удаления коммитит строку журнала и намерение снять кортеж владения ОДНОЙ
// транзакцией, а кортеж снимает дренаж, асинхронно и обычно за доли секунды.
// Значит построчный вопрос «вправе ли вызывающий видеть эту машину» получает
// «нет» ЗАКОННО — и получает его практически всегда.
//
// Спрашивать так — один из двух негодных исходов, и контракт формы называет оба
// прямо: «спрашивать модель прав про несуществующий объект либо не показывать
// удаления вовсе. Второе наступает ТИХО, и цена его — та самая, ради которой
// заводится подписка: потребитель, снявший поллинг, никогда не узнает об
// удалении и будет держать удалённые строки вечно». Именно поэтому якорь проекта
// стоит полем ОБОЛОЧКИ события и назван АВТОРИЗУЕМЫМ: решение о показе снятия
// принимается по нему, БЕЗ обращения к предмету.
//
// Наблюдалось до этой ветки: подписчик, которому разрешён проект и уже не
// разрешена машина, не получал события снятия ВОВСЕ — поток оставался открытым и
// молчал до истечения срока. Ни ошибки, ни пропуска в нумерации.
//
// # Что это РАСШИРЯЕТ, сказано вслух
//
// Вызывающий, которому разрешён проект, но НЕ был разрешён предмет, узнаёт из
// снятия, что предмет такого вида с таким идентификатором в проекте
// существовал. Это цена, а не побочный эффект, и она осознанная: альтернатива —
// не показывать удаления никому, то есть отказ от предмета подписки. Границы
// расширения:
//
//   - оно касается ТОЛЬКО снятия. Создание и правка по-прежнему судятся
//     пообъектно, и вызывающий без права на предмет не видит ни одного;
//   - оно не выходит за проект: якорь пуст — судим пообъектно (ниже), и
//     историческая строка без якоря остаётся невидимой;
//   - у владельца без проектного измерения ([ProjectAbsent]) якоря нет вовсе,
//     и снятие судится пообъектно — то есть остаётся при прежнем поведении.
//
// # Второго вопроса к модели обычно НЕ БУДЕТ
//
// Подписка, назвавшая ось проекта, уже прошла стража на открытии потока — тот же
// объект, то же действие, те же отношения. Спрашивать второй раз про тот же
// проект значило бы платить вызовом за ответ, который уже получен.
func (s *Server) narrow(
	ctx context.Context,
	rows []Row,
	events []*subscriptionv1.SubscriptionEvent,
	filter Filter,
) ([]rowVerdict, error) {
	// Требование заявляется ТАМ, ГДЕ строки используются, а не только у входа, и
	// заявляется ЦЕЛИКОМ. Половина условия была бы хуже целого отсутствия
	// проверки: она закрывала бы ШУМНЫЙ подслучай (нет сужателя — паника) и
	// оставляла ТИХИЙ (сужатель есть, но пропускает всё насквозь).
	if s.cfg.Narrower == nil || !s.cfg.Narrower.Narrows() {
		return nil, status.Error(codes.PermissionDenied,
			"subscription is unavailable without per-event authorization")
	}

	// Снятия отделяются ДО вопроса к модели: их судит якорь, и класть их в
	// пообъектную партию значило бы задать про них вопрос, ответ на который
	// заведомо «нет».
	anchored, err := s.removalsAllowedByAnchor(ctx, events, filter)
	if err != nil {
		return nil, err
	}

	byKind := make(map[string][]string, len(s.cfg.Journal.Mapping.Kinds))
	for i, ev := range events {
		if ev == nil || anchored[i] != verdictUnjudged {
			continue
		}
		byKind[rows[i].Kind] = append(byKind[rows[i].Kind], rows[i].ID)
	}

	allowed := make(map[string]map[string]struct{}, len(byKind))
	for kind, ids := range byKind {
		binding := s.cfg.Journal.Mapping.Kinds[kind]
		seen := make(map[string]struct{}, len(ids))
		for start := 0; start < len(ids); start += visibilityBatch {
			end := start + visibilityBatch
			if end > len(ids) {
				end = len(ids)
			}
			visible, err := listnarrow.IDs(ctx, s.cfg.Narrower,
				binding.ObjectType, binding.Action, ids[start:end])
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil, err
				}
				return nil, err
			}
			for _, id := range visible {
				seen[id] = struct{}{}
			}
		}
		allowed[kind] = seen
	}

	out := make([]rowVerdict, len(events))
	for i, ev := range events {
		if ev == nil {
			out[i] = rowDrop
			continue
		}
		switch anchored[i] {
		case verdictAllowed:
			out[i] = rowDeliver
			continue
		case verdictRefused:
			out[i] = rowDrop
			continue
		}
		if _, ok := allowed[rows[i].Kind][rows[i].ID]; ok {
			out[i] = rowDeliver
			continue
		}
		// Пообъектное «нет». Оно бывает ВРЕМЕННЫМ — кортеж владения кладётся
		// после фиксации ресурсной строки, — поэтому от [rowDrop] оно отделено:
		// решение, отдавать ли строку сейчас или подождать окно, принимает
		// вызывающий (см. `grant.go`), и слить два «нет» в одно значило бы
		// отнять у него этот выбор.
		out[i] = rowWithhold
	}
	return out, nil
}

// rowVerdict — что делать со строкой партии. Состояния ТРИ, и третье не сводится
// к второму: «не отдаём сейчас» и «не отдадим никогда» ведут к разным действиям
// сервера, а слитые в одно дают либо потерю события, либо вечное удержание.
type rowVerdict uint8

const (
	// rowDrop — строка не будет доставлена НИКОГДА: у неё нет типа объекта, рода
	// изменения или якоря, либо якорь указывает на чужой проект.
	rowDrop rowVerdict = iota
	// rowDeliver — модель сказала «да».
	rowDeliver
	// rowWithhold — модель сказала «нет» ПООБЪЕКТНО. Ответ может стать
	// положительным, когда владелец доложит кортеж владения.
	rowWithhold
)

// anchorVerdict — исход суждения ПО ЯКОРЮ. Состояния три, и «не судили» названо
// отдельно от «отказано»: слить их значило бы отдать пообъектной ветке строку,
// про которую решение уже принято, либо тихо потерять ту, про которую не
// принято.
type anchorVerdict uint8

const (
	verdictUnjudged anchorVerdict = iota
	verdictAllowed
	verdictRefused
)

// removalsAllowedByAnchor судит СНЯТИЯ по проектному якорю события.
//
// Возвращает вердикт на КАЖДУЮ позицию батча: соответствие позиционное, как и у
// [Server.mapRows], потому что дальше вердикты сопоставляются со строками по
// индексу.
//
// Не-снятия и строки без якоря остаются несуждёнными — их судит пообъектная
// ветка. Отказ модели прекращает обработку батча целиком: недоступный вердикт
// НЕ есть «да».
func (s *Server) removalsAllowedByAnchor(
	ctx context.Context,
	events []*subscriptionv1.SubscriptionEvent,
	filter Filter,
) ([]anchorVerdict, error) {
	out := make([]anchorVerdict, len(events))

	// У владельца без проектного измерения якоря нет вовсе — судить нечем, и
	// снятие остаётся при пообъектном суждении.
	if s.cfg.Journal.Storage.Project == ProjectAbsent {
		return out, nil
	}

	memo := make(map[string]bool, 2)
	for i, ev := range events {
		if ev == nil || ev.GetChange() != subscriptionv1.SubscriptionEvent_DELETED {
			continue
		}
		project := ev.GetProjectId()
		if project == "" {
			// Якоря нет — судить по нему нельзя. Такая строка (историческая, до
			// заведения якоря у владельца) остаётся пообъектной и невидимой; это
			// названо в миграции владельца и здесь не смягчается.
			continue
		}
		// Ось названа и уже одобрена стражем на открытии потока — тот же объект,
		// то же действие, те же отношения.
		if filter.ProjectID != "" && project == filter.ProjectID {
			out[i] = verdictAllowed
			continue
		}
		allowed, ok := memo[project]
		if !ok {
			var gerr error
			allowed, gerr = listnarrow.AllowedOnObject(ctx, s.cfg.Narrower,
				s.cfg.ProjectGate.ObjectType, s.cfg.ProjectGate.Action,
				s.cfg.ProjectGate.Relations, project)
			if gerr != nil {
				return nil, gerr
			}
			memo[project] = allowed
		}
		if allowed {
			out[i] = verdictAllowed
		} else {
			out[i] = verdictRefused
		}
	}
	return out, nil
}
