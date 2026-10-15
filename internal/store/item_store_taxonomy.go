// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// ItemName is the identity and display name of one item.
type ItemName struct {
	ID   dto.UUID
	Kind item.Kind
	Name *string
}

// QueryItemNames returns the id, kind, and name of every item the query
// matches, in no defined order. It ignores paging and loads no satellites.
func (s *ItemStore) QueryItemNames(ctx context.Context, query ItemQuery) ([]ItemName, error) {
	var out []ItemName
	err := s.queryMatched(ctx, query, func(idSQL string, args []any) (string, []any) {
		return "SELECT id, kind, name FROM items WHERE id IN (" + idSQL + ")", args
	}, func(rows *sql.Rows) error {
		var raw, name any
		var kind string
		if err := rows.Scan(&raw, &kind, &name); err != nil {
			return err
		}
		id, err := scanUUID(raw)
		if err != nil {
			return err
		}
		parsed, err := item.ParseKind(kind)
		if err != nil {
			return err
		}
		value := ItemName{ID: id, Kind: parsed}
		if !isNil(name) {
			text := asString(name)
			value.Name = &text
		}
		out = append(out, value)
		return nil
	})
	return out, err
}

// QueryPersonCredits returns the distinct person, name, and type of every
// credit on the items the query matches, in no defined order. Role and sort
// order are not read.
func (s *ItemStore) QueryPersonCredits(ctx context.Context, query ItemQuery) ([]item.PersonCredit, error) {
	var out []item.PersonCredit
	err := s.queryMatched(ctx, query, func(idSQL string, args []any) (string, []any) {
		return "SELECT credit.person_id, p.name, credit.type FROM (SELECT DISTINCT m.person_id, m.type FROM people_map m WHERE m.item_id IN (" + idSQL + ")) AS credit JOIN people p ON p.id = credit.person_id", args
	}, func(rows *sql.Rows) error {
		var raw any
		var name, typ string
		if err := rows.Scan(&raw, &name, &typ); err != nil {
			return err
		}
		id, err := scanUUID(raw)
		if err != nil {
			return err
		}
		out = append(out, item.PersonCredit{PersonID: id, Name: name, Type: personKind(typ)})
		return nil
	})
	return out, err
}

// QueryItemValues returns the distinct stored values of the given value kinds
// ("genre", "studio", "artist", "album_artist", ...) on the items the query
// matches, in no defined order.
func (s *ItemStore) QueryItemValues(ctx context.Context, query ItemQuery, kinds ...string) ([]string, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	var out []string
	err := s.queryMatched(ctx, query, s.itemValuesStatement(kinds), func(rows *sql.Rows) error {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		out = append(out, value)
		return nil
	})
	return out, err
}

// QueryStreamLanguages returns the distinct languages of the media streams
// of streamType ("Audio", "Subtitle") on the items the query matches, in no
// defined order. A stream without a language reads as "und" (undetermined),
// as BaseItemRepository.GetMediaStreamLanguages does
// (upstream/jellyfin/Jellyfin.Server.Implementations/Item/BaseItemRepository.ByName.cs:86-119).
func (s *ItemStore) QueryStreamLanguages(ctx context.Context, query ItemQuery, streamType string) ([]string, error) {
	var out []string
	err := s.queryMatched(ctx, query, s.streamLanguagesStatement(streamType), func(rows *sql.Rows) error {
		var language string
		if err := rows.Scan(&language); err != nil {
			return err
		}
		out = append(out, language)
		return nil
	})
	return out, err
}

// TranslateStreamLanguages returns the statement and arguments that
// QueryStreamLanguages runs for query, for plan tests.
func (s *ItemStore) TranslateStreamLanguages(ctx context.Context, query ItemQuery, streamType string) (string, []any, error) {
	return s.translateMatched(ctx, query, s.streamLanguagesStatement(streamType))
}

// TranslateItemValues returns the statement and arguments that
// QueryItemValues runs for query, for plan tests.
func (s *ItemStore) TranslateItemValues(ctx context.Context, query ItemQuery, kinds ...string) (string, []any, error) {
	return s.translateMatched(ctx, query, s.itemValuesStatement(kinds))
}

// itemValuesStatement and streamLanguagesStatement drive the read from the
// matched ids. SQLite does not reorder a CROSS JOIN, so each id reads its own
// value or stream rows through the primary key. With "item_id IN (ids)" the
// planner instead walked every row of the value kind or stream type in the
// database and probed the id list per row.
func (s *ItemStore) itemValuesStatement(kinds []string) func(string, []any) (string, []any) {
	return func(idSQL string, args []any) (string, []any) {
		marks := make([]string, len(kinds))
		for index, kind := range kinds {
			args = append(args, kind)
			marks[index] = s.ph(len(args))
		}
		return "SELECT DISTINCT v.value FROM (" + idSQL + ") AS scope CROSS JOIN item_value_map m JOIN item_values v ON v.id = m.value_id WHERE m.item_id = scope.id AND v.kind IN (" + strings.Join(marks, ", ") + ")", args
	}
}

func (s *ItemStore) streamLanguagesStatement(streamType string) func(string, []any) (string, []any) {
	return func(idSQL string, args []any) (string, []any) {
		args = append(args, streamType)
		return "SELECT DISTINCT CASE WHEN stream.language IS NULL OR stream.language = '' THEN 'und' ELSE stream.language END FROM (" + idSQL + ") AS scope CROSS JOIN media_streams AS stream WHERE stream.item_id = scope.id AND stream.stream_type = " + s.ph(len(args)), args
	}
}

// translateMatched returns the statement that queryMatched runs, in a reader
// transaction of its own.
func (s *ItemStore) translateMatched(ctx context.Context, query ItemQuery, build func(string, []any) (string, []any)) (_ string, _ []any, err error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return "", nil, err
	}
	defer rollbackOnReturn(tx, &err)
	_, translated, err := s.translateQuery(ctx, tx, query)
	if err != nil {
		return "", nil, err
	}
	statement, args := build(translated.IDSQL, append([]any(nil), translated.CountArgs...))
	return statement, args, tx.Commit()
}

// queryMatched runs one statement over the ids the query matches, in a
// reader transaction. build receives the id subquery and its arguments and
// returns the statement and its complete arguments.
func (s *ItemStore) queryMatched(ctx context.Context, query ItemQuery, build func(string, []any) (string, []any), scan func(*sql.Rows) error) (err error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	_, translated, err := s.translateQuery(ctx, tx, query)
	if err != nil {
		return err
	}
	statement, args := build(translated.IDSQL, append([]any(nil), translated.CountArgs...))
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		if err := scan(rows); err != nil {
			return closeRows(rows, err)
		}
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return err
	}
	return tx.Commit()
}
