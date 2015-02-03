// Copyright 2013 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ast

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	clientmodel "github.com/prometheus/client_golang/model"

	"github.com/prometheus/prometheus/stats"
	"github.com/prometheus/prometheus/storage/local"
	"github.com/prometheus/prometheus/utility"
)

// OutputFormat is an enum for the possible output formats.
type OutputFormat int

// Possible output formats.
const (
	Text OutputFormat = iota
	JSON
)

const jsonFormatVersion = 1

func (opType BinOpType) String() string {
	opTypeMap := map[BinOpType]string{
		Add: "+",
		Sub: "-",
		Mul: "*",
		Div: "/",
		Mod: "%",
		GT:  ">",
		LT:  "<",
		EQ:  "==",
		NE:  "!=",
		GE:  ">=",
		LE:  "<=",
		And: "AND",
		Or:  "OR",
	}
	return opTypeMap[opType]
}

func (aggrType AggrType) String() string {
	aggrTypeMap := map[AggrType]string{
		Sum:   "SUM",
		Avg:   "AVG",
		Min:   "MIN",
		Max:   "MAX",
		Count: "COUNT",
	}
	return aggrTypeMap[aggrType]
}

func (exprType ExprType) String() string {
	exprTypeMap := map[ExprType]string{
		ScalarType: "scalar",
		VectorType: "vector",
		MatrixType: "matrix",
		StringType: "string",
	}
	return exprTypeMap[exprType]
}

func (vector Vector) String() string {
	metricStrings := make([]string, 0, len(vector))
	for _, sample := range vector {
		metricStrings = append(metricStrings,
			fmt.Sprintf("%s => %v @[%v]",
				sample.Metric,
				sample.Value, sample.Timestamp))
	}
	return strings.Join(metricStrings, "\n")
}

func (matrix Matrix) String() string {
	metricStrings := make([]string, 0, len(matrix))
	for _, sampleStream := range matrix {
		metricName, hasName := sampleStream.Metric.Metric[clientmodel.MetricNameLabel]
		numLabels := len(sampleStream.Metric.Metric)
		if hasName {
			numLabels--
		}
		labelStrings := make([]string, 0, numLabels)
		for label, value := range sampleStream.Metric.Metric {
			if label != clientmodel.MetricNameLabel {
				labelStrings = append(labelStrings, fmt.Sprintf("%s=%q", label, value))
			}
		}
		sort.Strings(labelStrings)
		valueStrings := make([]string, 0, len(sampleStream.Values))
		for _, value := range sampleStream.Values {
			valueStrings = append(valueStrings,
				fmt.Sprintf("\n%v @[%v]", value.Value, value.Timestamp))
		}
		metricStrings = append(metricStrings,
			fmt.Sprintf("%s{%s} => %s",
				metricName,
				strings.Join(labelStrings, ", "),
				strings.Join(valueStrings, ", ")))
	}
	sort.Strings(metricStrings)
	return strings.Join(metricStrings, "\n")
}

// ErrorToJSON converts the given error into JSON.
func ErrorToJSON(err error) string {
	errorStruct := struct {
		Type    string `json:"type"`
		Value   string `json:"value"`
		Version int    `json:"version"`
	}{
		Type:    "error",
		Value:   err.Error(),
		Version: jsonFormatVersion,
	}

	errorJSON, err := json.Marshal(errorStruct)
	if err != nil {
		return ""
	}
	return string(errorJSON)
}

// TypedValueToJSON converts the given data of type 'scalar',
// 'vector', or 'matrix' into its JSON representation.
func TypedValueToJSON(data interface{}, typeStr string) string {
	dataStruct := struct {
		Type    string      `json:"type"`
		Value   interface{} `json:"value"`
		Version int         `json:"version"`
	}{
		Type:    typeStr,
		Value:   data,
		Version: jsonFormatVersion,
	}
	dataJSON, err := json.Marshal(dataStruct)
	if err != nil {
		return ErrorToJSON(err)
	}
	return string(dataJSON)
}

// EvalToString evaluates the given node into a string of the given format.
func EvalToString(node Node, timestamp clientmodel.Timestamp, format OutputFormat, storage local.Storage, queryStats *stats.TimerGroup) string {
	totalEvalTimer := queryStats.GetTimer(stats.TotalEvalTime).Start()
	defer totalEvalTimer.Stop()

	prepareTimer := queryStats.GetTimer(stats.TotalQueryPreparationTime).Start()
	closer, err := prepareInstantQuery(node, timestamp, storage, queryStats)
	prepareTimer.Stop()
	if err != nil {
		panic(err)
	}
	defer closer.Close()

	evalTimer := queryStats.GetTimer(stats.InnerEvalTime).Start()
	switch node.Type() {
	case ScalarType:
		scalar := node.(ScalarNode).Eval(timestamp)
		evalTimer.Stop()
		switch format {
		case Text:
			return fmt.Sprintf("scalar: %v @[%v]", scalar, timestamp)
		case JSON:
			return TypedValueToJSON(scalar, "scalar")
		}
	case VectorType:
		vector := node.(VectorNode).Eval(timestamp)
		evalTimer.Stop()
		switch format {
		case Text:
			return vector.String()
		case JSON:
			return TypedValueToJSON(vector, "vector")
		}
	case MatrixType:
		matrix := node.(MatrixNode).Eval(timestamp)
		evalTimer.Stop()
		switch format {
		case Text:
			return matrix.String()
		case JSON:
			return TypedValueToJSON(matrix, "matrix")
		}
	case StringType:
		str := node.(StringNode).Eval(timestamp)
		evalTimer.Stop()
		switch format {
		case Text:
			return str
		case JSON:
			return TypedValueToJSON(str, "string")
		}
	}
	panic("Switch didn't cover all node types")
}

// EvalToVector evaluates the given node into a Vector. Matrices aren't supported.
func EvalToVector(node Node, timestamp clientmodel.Timestamp, storage local.Storage, queryStats *stats.TimerGroup) (Vector, error) {
	totalEvalTimer := queryStats.GetTimer(stats.TotalEvalTime).Start()
	defer totalEvalTimer.Stop()

	prepareTimer := queryStats.GetTimer(stats.TotalQueryPreparationTime).Start()
	closer, err := prepareInstantQuery(node, timestamp, storage, queryStats)
	prepareTimer.Stop()
	if err != nil {
		panic(err)
	}
	defer closer.Close()

	evalTimer := queryStats.GetTimer(stats.InnerEvalTime).Start()
	switch node.Type() {
	case ScalarType:
		scalar := node.(ScalarNode).Eval(timestamp)
		evalTimer.Stop()
		return Vector{&Sample{Value: scalar}}, nil
	case VectorType:
		vector := node.(VectorNode).Eval(timestamp)
		evalTimer.Stop()
		return vector, nil
	case MatrixType:
		return nil, errors.New("matrices not supported by EvalToVector")
	case StringType:
		str := node.(StringNode).Eval(timestamp)
		evalTimer.Stop()
		return Vector{
			&Sample{
				Metric: clientmodel.COWMetric{
					Metric: clientmodel.Metric{
						"__value__": clientmodel.LabelValue(str),
					},
					Copied: true,
				},
			},
		}, nil
	}
	panic("Switch didn't cover all node types")
}

// NodeTreeToDotGraph returns a DOT representation of the scalar
// literal.
func (node *ScalarLiteral) NodeTreeToDotGraph() string {
	return fmt.Sprintf("%#p[label=\"%v\"];\n", node, node.value)
}

func functionArgsToDotGraph(node Node, args []Node) string {
	graph := ""
	for _, arg := range args {
		graph += fmt.Sprintf("%x -> %x;\n", reflect.ValueOf(node).Pointer(), reflect.ValueOf(arg).Pointer())
	}
	for _, arg := range args {
		graph += arg.NodeTreeToDotGraph()
	}
	return graph
}

// NodeTreeToDotGraph returns a DOT representation of the function
// call.
func (node *ScalarFunctionCall) NodeTreeToDotGraph() string {
	graph := fmt.Sprintf("%#p[label=\"%s\"];\n", node, node.function.name)
	graph += functionArgsToDotGraph(node, node.args)
	return graph
}

// NodeTreeToDotGraph returns a DOT representation of the expression.
func (node *ScalarArithExpr) NodeTreeToDotGraph() string {
	nodeAddr := reflect.ValueOf(node).Pointer()
	graph := fmt.Sprintf(
		`
		%x[label="%s"];
		%x -> %x;
		%x -> %x;
		%s
		%s
	}`,
		nodeAddr, node.opType,
		nodeAddr, reflect.ValueOf(node.lhs).Pointer(),
		nodeAddr, reflect.ValueOf(node.rhs).Pointer(),
		node.lhs.NodeTreeToDotGraph(),
		node.rhs.NodeTreeToDotGraph(),
	)
	return graph
}

// NodeTreeToDotGraph returns a DOT representation of the vector selector.
func (node *VectorSelector) NodeTreeToDotGraph() string {
	return fmt.Sprintf("%#p[label=\"%s\"];\n", node, node)
}

// NodeTreeToDotGraph returns a DOT representation of the function
// call.
func (node *VectorFunctionCall) NodeTreeToDotGraph() string {
	graph := fmt.Sprintf("%#p[label=\"%s\"];\n", node, node.function.name)
	graph += functionArgsToDotGraph(node, node.args)
	return graph
}

// NodeTreeToDotGraph returns a DOT representation of the vector
// aggregation.
func (node *VectorAggregation) NodeTreeToDotGraph() string {
	groupByStrings := make([]string, 0, len(node.groupBy))
	for _, label := range node.groupBy {
		groupByStrings = append(groupByStrings, string(label))
	}

	graph := fmt.Sprintf("%#p[label=\"%s BY (%s)\"]\n",
		node,
		node.aggrType,
		strings.Join(groupByStrings, ", "))
	graph += fmt.Sprintf("%#p -> %x;\n", node, reflect.ValueOf(node.vector).Pointer())
	graph += node.vector.NodeTreeToDotGraph()
	return graph
}

// NodeTreeToDotGraph returns a DOT representation of the expression.
func (node *VectorArithExpr) NodeTreeToDotGraph() string {
	nodeAddr := reflect.ValueOf(node).Pointer()
	graph := fmt.Sprintf(
		`
		%x[label="%s"];
		%x -> %x;
		%x -> %x;
		%s
		%s
	}`,
		nodeAddr, node.opType,
		nodeAddr, reflect.ValueOf(node.lhs).Pointer(),
		nodeAddr, reflect.ValueOf(node.rhs).Pointer(),
		node.lhs.NodeTreeToDotGraph(),
		node.rhs.NodeTreeToDotGraph(),
	)
	return graph
}

// NodeTreeToDotGraph returns a DOT representation of the matrix
// selector.
func (node *MatrixSelector) NodeTreeToDotGraph() string {
	return fmt.Sprintf("%#p[label=\"%s\"];\n", node, node)
}

// NodeTreeToDotGraph returns a DOT representation of the string
// literal.
func (node *StringLiteral) NodeTreeToDotGraph() string {
	return fmt.Sprintf("%#p[label=\"'%q'\"];\n", node, node.str)
}

// NodeTreeToDotGraph returns a DOT representation of the function
// call.
func (node *StringFunctionCall) NodeTreeToDotGraph() string {
	graph := fmt.Sprintf("%#p[label=\"%s\"];\n", node, node.function.name)
	graph += functionArgsToDotGraph(node, node.args)
	return graph
}

func (nodes Nodes) String() string {
	nodeStrings := make([]string, 0, len(nodes))
	for _, node := range nodes {
		nodeStrings = append(nodeStrings, node.String())
	}
	return strings.Join(nodeStrings, ", ")
}

func (node *ScalarLiteral) String() string {
	return fmt.Sprint(node.value)
}

func (node *ScalarFunctionCall) String() string {
	return fmt.Sprintf("%s(%s)", node.function.name, node.args)
}

func (node *ScalarArithExpr) String() string {
	return fmt.Sprintf("(%s %s %s)", node.lhs, node.opType, node.rhs)
}

func (node *VectorSelector) String() string {
	labelStrings := make([]string, 0, len(node.labelMatchers)-1)
	var metricName clientmodel.LabelValue
	for _, matcher := range node.labelMatchers {
		if matcher.Name != clientmodel.MetricNameLabel {
			labelStrings = append(labelStrings, fmt.Sprintf("%s%s%q", matcher.Name, matcher.Type, matcher.Value))
		} else {
			metricName = matcher.Value
		}
	}

	switch len(labelStrings) {
	case 0:
		return string(metricName)
	default:
		sort.Strings(labelStrings)
		return fmt.Sprintf("%s{%s}", metricName, strings.Join(labelStrings, ","))
	}
}

func (node *VectorFunctionCall) String() string {
	return fmt.Sprintf("%s(%s)", node.function.name, node.args)
}

func (node *VectorAggregation) String() string {
	aggrString := fmt.Sprintf("%s(%s)", node.aggrType, node.vector)
	if len(node.groupBy) > 0 {
		return fmt.Sprintf("%s BY (%s)", aggrString, node.groupBy)
	}
	return aggrString
}

func (node *VectorArithExpr) String() string {
	return fmt.Sprintf("(%s %s %s)", node.lhs, node.opType, node.rhs)
}

func (node *MatrixSelector) String() string {
	vectorString := (&VectorSelector{labelMatchers: node.labelMatchers}).String()
	intervalString := fmt.Sprintf("[%s]", utility.DurationToString(node.interval))
	return vectorString + intervalString
}

func (node *StringLiteral) String() string {
	return fmt.Sprintf("%q", node.str)
}

func (node *StringFunctionCall) String() string {
	return fmt.Sprintf("%s(%s)", node.function.name, node.args)
}
