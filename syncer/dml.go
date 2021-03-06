// Copyright 2019 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package syncer

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/pingcap/dm/pkg/log"
	"github.com/pingcap/errors"
)

func genInsertSQLs(schema string, table string, dataSeq [][]interface{}, columns []*column, indexColumns map[string][]*column) ([]string, [][]string, [][]interface{}, error) {
	sqls := make([]string, 0, len(dataSeq))
	keys := make([][]string, 0, len(dataSeq))
	values := make([][]interface{}, 0, len(dataSeq))
	columnList := genColumnList(columns)
	columnPlaceholders := genColumnPlaceholders(len(columns))
	for _, data := range dataSeq {
		if len(data) != len(columns) {
			return nil, nil, nil, errors.Errorf("insert columns and data mismatch in length: %d (columns) vs %d (data)", len(columns), len(data))
		}

		value := make([]interface{}, 0, len(data))
		for i := range data {
			value = append(value, castUnsigned(data[i], columns[i].unsigned, columns[i].tp))
		}

		sql := fmt.Sprintf("REPLACE INTO `%s`.`%s` (%s) VALUES (%s);", schema, table, columnList, columnPlaceholders)
		ks := genMultipleKeys(columns, value, indexColumns)
		sqls = append(sqls, sql)
		values = append(values, value)
		keys = append(keys, ks)
	}

	return sqls, keys, values, nil
}

func genUpdateSQLs(schema string, table string, data [][]interface{}, columns []*column, indexColumns map[string][]*column, safeMode bool) ([]string, [][]string, [][]interface{}, error) {
	sqls := make([]string, 0, len(data)/2)
	keys := make([][]string, 0, len(data)/2)
	values := make([][]interface{}, 0, len(data)/2)
	columnList := genColumnList(columns)
	columnPlaceholders := genColumnPlaceholders(len(columns))
	defaultIndexColumns := findFitIndex(indexColumns)

	for i := 0; i < len(data); i += 2 {
		oldData := data[i]
		changedData := data[i+1]

		if len(oldData) != len(changedData) {
			return nil, nil, nil, errors.Errorf("update data mismatch in length: %d (columns) vs %d (data)", len(oldData), len(changedData))
		}

		if len(oldData) != len(columns) {
			return nil, nil, nil, errors.Errorf("update columns and data mismatch in length: %d (columns) vs %d (data)", len(columns), len(oldData))
		}

		oldValues := make([]interface{}, 0, len(oldData))
		for i := range oldData {
			oldValues = append(oldValues, castUnsigned(oldData[i], columns[i].unsigned, columns[i].tp))
		}
		changedValues := make([]interface{}, 0, len(changedData))
		for i := range changedData {
			changedValues = append(changedValues, castUnsigned(changedData[i], columns[i].unsigned, columns[i].tp))
		}

		if len(defaultIndexColumns) == 0 {
			defaultIndexColumns = getAvailableIndexColumn(indexColumns, oldValues)
		}

		ks := genMultipleKeys(columns, oldValues, indexColumns)
		ks = append(ks, genMultipleKeys(columns, changedValues, indexColumns)...)

		if safeMode {
			// generate delete sql from old data
			sql, value := genDeleteSQL(schema, table, oldValues, columns, defaultIndexColumns)
			sqls = append(sqls, sql)
			values = append(values, value)
			keys = append(keys, ks)
			// generate replace sql from new data
			sql = fmt.Sprintf("REPLACE INTO `%s`.`%s` (%s) VALUES (%s);", schema, table, columnList, columnPlaceholders)
			sqls = append(sqls, sql)
			values = append(values, changedValues)
			keys = append(keys, ks)
			continue
		}

		updateColumns := make([]*column, 0, len(defaultIndexColumns))
		updateValues := make([]interface{}, 0, len(defaultIndexColumns))
		for j := range oldValues {
			updateColumns = append(updateColumns, columns[j])
			updateValues = append(updateValues, changedValues[j])
		}

		// ignore no changed sql
		if len(updateColumns) == 0 {
			continue
		}

		value := make([]interface{}, 0, len(oldData))
		kvs := genKVs(updateColumns)
		value = append(value, updateValues...)

		whereColumns, whereValues := columns, oldValues
		if len(defaultIndexColumns) > 0 {
			whereColumns, whereValues = getColumnData(columns, defaultIndexColumns, oldValues)
		}

		where := genWhere(whereColumns, whereValues)
		value = append(value, whereValues...)

		sql := fmt.Sprintf("UPDATE `%s`.`%s` SET %s WHERE %s LIMIT 1;", schema, table, kvs, where)
		sqls = append(sqls, sql)
		values = append(values, value)
		keys = append(keys, ks)
	}

	return sqls, keys, values, nil
}

func genDeleteSQLs(schema string, table string, dataSeq [][]interface{}, columns []*column, indexColumns map[string][]*column) ([]string, [][]string, [][]interface{}, error) {
	sqls := make([]string, 0, len(dataSeq))
	keys := make([][]string, 0, len(dataSeq))
	values := make([][]interface{}, 0, len(dataSeq))
	defaultIndexColumns := findFitIndex(indexColumns)

	for _, data := range dataSeq {
		if len(data) != len(columns) {
			return nil, nil, nil, errors.Errorf("delete columns and data mismatch in length: %d (columns) vs %d (data)", len(columns), len(data))
		}

		value := make([]interface{}, 0, len(data))
		for i := range data {
			value = append(value, castUnsigned(data[i], columns[i].unsigned, columns[i].tp))
		}

		if len(defaultIndexColumns) == 0 {
			defaultIndexColumns = getAvailableIndexColumn(indexColumns, value)
		}
		ks := genMultipleKeys(columns, value, indexColumns)

		sql, value := genDeleteSQL(schema, table, value, columns, defaultIndexColumns)
		sqls = append(sqls, sql)
		values = append(values, value)
		keys = append(keys, ks)
	}

	return sqls, keys, values, nil
}

func genDeleteSQL(schema string, table string, value []interface{}, columns []*column, indexColumns []*column) (string, []interface{}) {
	whereColumns, whereValues := columns, value
	if len(indexColumns) > 0 {
		whereColumns, whereValues = getColumnData(columns, indexColumns, value)
	}

	where := genWhere(whereColumns, whereValues)
	sql := fmt.Sprintf("DELETE FROM `%s`.`%s` WHERE %s LIMIT 1;", schema, table, where)

	return sql, whereValues
}

func genColumnList(columns []*column) string {
	var columnList []byte
	for i, column := range columns {
		name := fmt.Sprintf("`%s`", column.name)
		columnList = append(columnList, []byte(name)...)

		if i != len(columns)-1 {
			columnList = append(columnList, ',')
		}
	}

	return string(columnList)
}

func genColumnPlaceholders(length int) string {
	values := make([]string, length, length)
	for i := 0; i < length; i++ {
		values[i] = "?"
	}
	return strings.Join(values, ",")
}

func castUnsigned(data interface{}, unsigned bool, tp string) interface{} {
	if !unsigned {
		return data
	}

	switch v := data.(type) {
	case int:
		return uint(v)
	case int8:
		return uint8(v)
	case int16:
		return uint16(v)
	case int32:
		if strings.Contains(strings.ToLower(tp), "mediumint") {
			// we use int32 to store MEDIUMINT, if the value is signed, it's fine
			// but if the value is un-signed, simply convert it use `uint32` may out of the range
			// like -4692783 converted to 4290274513 (2^32 - 4692783), but we expect 12084433 (2^24 - 4692783)
			data := make([]byte, 4)
			binary.LittleEndian.PutUint32(data, uint32(v))
			return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
		}
		return uint32(v)
	case int64:
		return strconv.FormatUint(uint64(v), 10)
	}

	return data
}

func columnValue(value interface{}, unsigned bool, tp string) string {
	castValue := castUnsigned(value, unsigned, tp)

	var data string
	switch v := castValue.(type) {
	case nil:
		data = "null"
	case bool:
		if v {
			data = "1"
		} else {
			data = "0"
		}
	case int:
		data = strconv.FormatInt(int64(v), 10)
	case int8:
		data = strconv.FormatInt(int64(v), 10)
	case int16:
		data = strconv.FormatInt(int64(v), 10)
	case int32:
		data = strconv.FormatInt(int64(v), 10)
	case int64:
		data = strconv.FormatInt(int64(v), 10)
	case uint8:
		data = strconv.FormatUint(uint64(v), 10)
	case uint16:
		data = strconv.FormatUint(uint64(v), 10)
	case uint32:
		data = strconv.FormatUint(uint64(v), 10)
	case uint64:
		data = strconv.FormatUint(uint64(v), 10)
	case float32:
		data = strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		data = strconv.FormatFloat(float64(v), 'f', -1, 64)
	case string:
		data = v
	case []byte:
		data = string(v)
	default:
		data = fmt.Sprintf("%v", v)
	}

	return data
}

func findColumn(columns []*column, indexColumn string) *column {
	for _, column := range columns {
		if column.name == indexColumn {
			return column
		}
	}

	return nil
}

func findColumns(columns []*column, indexColumns map[string][]string) map[string][]*column {
	result := make(map[string][]*column)

	for keyName, indexCols := range indexColumns {
		cols := make([]*column, 0, len(indexCols))
		for _, name := range indexCols {
			column := findColumn(columns, name)
			if column != nil {
				cols = append(cols, column)
			}
		}
		result[keyName] = cols
	}

	return result
}

func genKeyList(columns []*column, dataSeq []interface{}) string {
	values := make([]string, 0, len(dataSeq))
	for i, data := range dataSeq {
		values = append(values, columnValue(data, columns[i].unsigned, columns[i].tp))
	}

	return strings.Join(values, ",")
}

func genMultipleKeys(columns []*column, value []interface{}, indexColumns map[string][]*column) []string {
	var multipleKeys []string
	for _, indexCols := range indexColumns {
		cols, vals := getColumnData(columns, indexCols, value)
		multipleKeys = append(multipleKeys, genKeyList(cols, vals))
	}
	return multipleKeys
}

func findFitIndex(indexColumns map[string][]*column) []*column {
	cols, ok := indexColumns["primary"]
	if ok {
		if len(cols) == 0 {
			log.Error("cols is empty")
		} else {
			return cols
		}
	}

	// second find not null unique key
	fn := func(c *column) bool {
		return !c.NotNull
	}

	return getSpecifiedIndexColumn(indexColumns, fn)
}

func getAvailableIndexColumn(indexColumns map[string][]*column, data []interface{}) []*column {
	fn := func(c *column) bool {
		return data[c.idx] == nil
	}

	return getSpecifiedIndexColumn(indexColumns, fn)
}

func getSpecifiedIndexColumn(indexColumns map[string][]*column, fn func(col *column) bool) []*column {
	for _, indexCols := range indexColumns {
		if len(indexCols) == 0 {
			continue
		}

		findFitIndex := true
		for _, col := range indexCols {
			if fn(col) {
				findFitIndex = false
				break
			}
		}

		if findFitIndex {
			return indexCols
		}
	}

	return nil
}

func getColumnData(columns []*column, indexColumns []*column, data []interface{}) ([]*column, []interface{}) {
	cols := make([]*column, 0, len(columns))
	values := make([]interface{}, 0, len(columns))
	for _, column := range indexColumns {
		cols = append(cols, column)
		values = append(values, data[column.idx])
	}

	return cols, values
}

func genWhere(columns []*column, data []interface{}) string {
	var kvs bytes.Buffer
	for i := range columns {
		kvSplit := "="
		if data[i] == nil {
			kvSplit = "IS"
		}

		if i == len(columns)-1 {
			fmt.Fprintf(&kvs, "`%s` %s ?", columns[i].name, kvSplit)
		} else {
			fmt.Fprintf(&kvs, "`%s` %s ? AND ", columns[i].name, kvSplit)
		}
	}

	return kvs.String()
}

func genKVs(columns []*column) string {
	var kvs bytes.Buffer
	for i := range columns {
		if i == len(columns)-1 {
			fmt.Fprintf(&kvs, "`%s` = ?", columns[i].name)
		} else {
			fmt.Fprintf(&kvs, "`%s` = ?, ", columns[i].name)
		}
	}

	return kvs.String()
}

func (s *Syncer) mappingDML(schema, table string, columns []string, data [][]interface{}) ([][]interface{}, error) {
	if s.columnMapping == nil {
		return data, nil
	}
	var (
		err  error
		rows = make([][]interface{}, len(data))
	)
	for i := range data {
		rows[i], _, err = s.columnMapping.HandleRowValue(schema, table, columns, data[i])
		if err != nil {
			return nil, errors.Trace(err)
		}
	}
	return rows, nil
}
