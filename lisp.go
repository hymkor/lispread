package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/hymkor/gmnlisp"
)

type referCell struct {
	f func(context.Context, *gmnlisp.World, int, int) (string, error)
}

func (rc referCell) call(ctx context.Context, w *gmnlisp.World, args []gmnlisp.Node) (string, error) {
	relRow, ok := args[0].(gmnlisp.Integer)
	if !ok {
		return "", gmnlisp.ErrExpectedNumber
	}
	relCol, ok := args[1].(gmnlisp.Integer)
	if !ok {
		return "", gmnlisp.ErrExpectedNumber
	}
	if rc.f == nil {
		return fmt.Sprintf("(func (RC %d %d) called)", relRow, relCol), nil
	}
	homeRow, ok := w.Dynamic(rowSymbol()).(gmnlisp.Integer)
	if !ok {
		return "", fmt.Errorf("(dynamic (row)): %w", gmnlisp.ErrExpectedNumber)
	}
	homeCol, ok := w.Dynamic(colSymbol()).(gmnlisp.Integer)
	if !ok {
		return "", fmt.Errorf("(dynamic (col)): %w", gmnlisp.ErrExpectedNumber)
	}
	return rc.f(ctx, w, int(homeRow+relRow), int(homeCol+relCol))
}

func (rc referCell) String(ctx context.Context, w *gmnlisp.World, args []gmnlisp.Node) (gmnlisp.Node, error) {
	s, err := rc.call(ctx, w, args)
	return gmnlisp.String(s), err
}

func (rc referCell) Integer(ctx context.Context, w *gmnlisp.World, args []gmnlisp.Node) (gmnlisp.Node, error) {
	s, err := rc.call(ctx, w, args)
	if err != nil {
		return gmnlisp.Null, err
	}
	val, err := strconv.Atoi(s)
	return gmnlisp.Integer(val), err
}

func (rc referCell) Float(ctx context.Context, w *gmnlisp.World, args []gmnlisp.Node) (gmnlisp.Node, error) {
	s, err := rc.call(ctx, w, args)
	if err != nil {
		return gmnlisp.Null, err
	}
	val, err := strconv.ParseFloat(s, 64)
	return gmnlisp.Float(val), err
}

var sumFunc = &gmnlisp.LispString{S: `
(lambda (r1 c1 r2 c2 ref)
	(if (> c1 c2)
		(let ((tmp c1))
			(setq c1 c2 c2 tmp)))
	(if (> r1 r2)
		(let ((tmp r1))
			(setq r1 r2 r2 tmp)))
	(let ((sum 0)(r r1))
		(while (<= r r2)
			(let ((c c1))
				(while (<= c c2)
					(setq sum (+ sum (funcall ref r c)))
					(setq c (1+ c))))
			(setq r (1+ r)))
		sum))
`}

var sumInteger = &gmnlisp.LispString{
	S: `(lambda (r1 c1 r2 c2) (sum_ r1 c1 r2 c2 #'rc%))`}

var sumFloat = &gmnlisp.LispString{
	S: `(lambda (r1 c1 r2 c2) (sum_ r1 c1 r2 c2 #'rc!))`}

var lisp = sync.OnceValue(gmnlisp.New)
var rowSymbol = sync.OnceValue(func() gmnlisp.Symbol {
	return gmnlisp.NewSymbol("row")
})
var colSymbol = sync.OnceValue(func() gmnlisp.Symbol {
	return gmnlisp.NewSymbol("col")
})

type Cell struct {
	source string
}

func (c Cell) Eval(ctx context.Context, row int, col int, refer func(context.Context, *gmnlisp.World, int, int) (string, error)) string {
	if len(c.source) <= 0 || c.source[0] != '(' {
		return c.source
	}

	rc := &referCell{f: refer}

	dynamics := lisp().NewDynamics()
	defer dynamics.Close()
	dynamics.Set(rowSymbol(), gmnlisp.Integer(row))
	dynamics.Set(colSymbol(), gmnlisp.Integer(col))

	L := lisp().Let(gmnlisp.Variables{
		gmnlisp.NewSymbol("rc"):   &gmnlisp.Function{C: 2, F: rc.String},
		gmnlisp.NewSymbol("rc%"):  &gmnlisp.Function{C: 2, F: rc.Integer},
		gmnlisp.NewSymbol("rc!"):  &gmnlisp.Function{C: 2, F: rc.Float},
		gmnlisp.NewSymbol("sum_"): sumFunc,
		gmnlisp.NewSymbol("sum%"): sumInteger,
		gmnlisp.NewSymbol("sum!"): sumFloat,
	})

	value, err := L.Interpret(ctx, c.source)
	if err != nil {
		return fmt.Sprintf("!`%s`: %s", c.source, err.Error())
	}
	return value.String()
}

func (c Cell) Empty() bool {
	return c.source == ""
}
