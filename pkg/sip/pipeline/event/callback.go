package event

import (
	"context"
	"fmt"
	"reflect"
)

func MakeCallback[Callback any]() (Callback, reflect.Value, reflect.Value) {
	typ := reflect.TypeFor[Callback]()

	// log.Debugw("Making callback", "type", typ)

	if typ.Kind() != reflect.Func {
		return *new(Callback), reflect.Value{}, reflect.Value{}
	}

	inT := typ.NumIn()
	outT := typ.NumOut()

	// log.Debugw("Making callback", "numInputs", inT)
	argStructFields := make([]reflect.StructField, inT)
	for i := 0; i < inT; i++ {
		argStructFields[i] = reflect.StructField{
			Name: fmt.Sprintf("Arg%d", i),
			Type: typ.In(i),
		}
	}

	argStructType := reflect.StructOf(argStructFields)
	// log.Debugw("Created args struct type", "type", argStructType)

	argChanType := reflect.ChanOf(reflect.BothDir, argStructType)
	argChan := reflect.MakeChan(argChanType, 1)

	// log.Debugw("Making callback", "numOutputs", outT)
	retStuctFields := make([]reflect.StructField, outT)
	for i := 0; i < outT; i++ {
		retStuctFields[i] = reflect.StructField{
			Name: fmt.Sprintf("Ret%d", i),
			Type: typ.Out(i),
		}
	}

	retStructType := reflect.StructOf(retStuctFields)

	retChanType := reflect.ChanOf(reflect.BothDir, retStructType)
	retChan := reflect.MakeChan(retChanType, 1)

	wrappedFunc := reflect.MakeFunc(typ, func(args []reflect.Value) (results []reflect.Value) {
		argStruct := reflect.New(argStructType).Elem()
		for i, arg := range args {
			argStruct.Field(i).Set(arg)
		}
		argChan.Send(argStruct)
		recv, ok := retChan.Recv()
		if !ok {
			results = make([]reflect.Value, outT)
			return results
		}

		retStruct := recv
		results = make([]reflect.Value, outT)
		for i := 0; i < outT; i++ {
			results[i] = retStruct.Field(i)
		}
		return results
	})

	return wrappedFunc.Interface().(Callback), argChan, retChan
}

func HandleCallback[Callback any](ctx context.Context, loop *EventLoop, cb Callback, args, ret reflect.Value) {
	val := reflect.ValueOf(cb)
	typ := reflect.TypeOf(cb)

	if typ.Kind() != reflect.Func {
		return
	}

	inT := typ.NumIn()
	outT := typ.NumOut()

	loop.Register(CallbackHandler{
		loop: loop,
		cb:   val,
		args: args,
		ret:  ret,
		inT:  inT,
		outT: outT,
		done: reflect.ValueOf(ctx.Done()),
	})
}

func RegisterCallback[Callback any](ctx context.Context, loop *EventLoop, cb Callback) Callback {
	wrap, args, ret := MakeCallback[Callback]()

	HandleCallback(ctx, loop, cb, args, ret)

	return wrap
}
