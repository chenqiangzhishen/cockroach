// Copyright 2015 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// Author: Tobias Schottdorf (tobias.schottdorf@gmail.com)
// Author: Andrei Matei (andreimatei1@gmail.com)
// Author: Radu Berinde (radu@cockroachlabs.com)

package tracing

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/net/context"
	"golang.org/x/net/trace"

	"github.com/cockroachdb/cockroach/pkg/settings"
	lightstep "github.com/lightstep/lightstep-tracer-go"
	opentracing "github.com/opentracing/opentracing-go"
)

// Snowball is set as Baggage on traces which are used for snowball tracing.
const Snowball = "sb"

// maxLogsPerSpan limits the number of logs in a Span; use a comfortable limit.
const maxLogsPerSpan = 1000

// These constants are used to form keys to represent tracing context
// information in carriers supporting opentracing.HTTPHeaders format.
// These must be identical to what lightstep uses (to allow us to inject the
// information into lightstep); see:
//   github.com/lightstep/lightstep-tracer-go/basictracer/propagation_ot.go
const (
	prefixTracerState = "ot-tracer-"
	prefixBaggage     = "ot-baggage-"

	fieldNameTraceID = prefixTracerState + "traceid"
	fieldNameSpanID  = prefixTracerState + "spanid"
	fieldNameSampled = prefixTracerState + "sampled"
)

var enableNetTrace = settings.RegisterBoolSetting(
	"trace.debug.enable",
	"if set, traces for recent requests can be seen in the /debug page",
	false,
)

var lightstepToken = settings.RegisterStringSetting(
	"trace.lightstep.token",
	"if set, traces go to Lightstep using this token",
	"",
)

// We don't call OnChange inline above because it causes an "initialization
// loop" compile error.
var _ = lightstepToken.OnChange(updateLightstep)

// Atomic pointer of type *opentracing.Tracer which itself points to a lightstep
// tracer. We don't use sync.Value because we can't set it to nil.
var lightstepPtr unsafe.Pointer

func updateLightstep() {
	if token := lightstepToken.Get(); token == "" {
		// TODO(radu): if we had a lightstep tracer allocated, its background task
		// will live on.
		// Filed https://github.com/lightstep/lightstep-tracer-go/issues/82.
		atomic.StorePointer(&lightstepPtr, nil)
	} else {
		lsTr := lightstep.NewTracer(lightstep.Options{
			AccessToken:    token,
			MaxLogsPerSpan: maxLogsPerSpan,
			UseGRPC:        true,
		})
		atomic.StorePointer(&lightstepPtr, unsafe.Pointer(&lsTr))
	}
}

func getLightstep() opentracing.Tracer {
	if ptr := atomic.LoadPointer(&lightstepPtr); ptr != nil {
		return *(*opentracing.Tracer)(ptr)
	}
	return nil
}

// Tracer is our own custom implementation of opentracing.Tracer. It supports:
//
//  - forwarding events to x/net/trace instances
//
//  - recording traces. Recording is started automatically for spans that have
//    the Snowball baggage and can be started explicitly as well. Recorded
//    events can be retrieved at any time.
//
//  - lightstep traces. This is implemented by maintaining a "shadow" lightstep
//    span inside each of our spans.
//
// Even when tracing is disabled, we still use this Tracer (with x/net/trace and
// lightstep disabled) because of its recording capability (snowball
// tracing needs to work in all cases).
//
// Tracer is currently stateless so we could have a single instance; however,
// this won't be the case if the cluster settings move away from using global
// state.
type Tracer struct {
	// Preallocated noopSpan, used to avoid creating spans when we are not using
	// x/net/trace or lightstep and we are not recording.
	noopSpan noopSpan
}

var _ opentracing.Tracer = &Tracer{}

// NewTracer creates a Tracer. The cluster settings control whether
// we trace to net/trace and/or lightstep.
func NewTracer() opentracing.Tracer {
	t := &Tracer{}
	t.noopSpan.tracer = t
	return t
}

// lightstepExtractIDsCarrier is used as a carrier for getLightstepSpanIDs.
type lightstepExtractIDsCarrier struct {
	traceID, spanID uint64
}

var _ opentracing.TextMapWriter = &lightstepExtractIDsCarrier{}

// Set is part of the opentracing.TextMapWriter interface.
func (l *lightstepExtractIDsCarrier) Set(key, val string) {
	var err error
	switch key {
	case fieldNameTraceID:
		l.traceID, err = strconv.ParseUint(val, 16, 64)
	case fieldNameSpanID:
		l.spanID, err = strconv.ParseUint(val, 16, 64)
	default:
		// Ignore all other keys.
		return
	}
	if err != nil {
		panic(err)
	}
}

// getLightstepSpanIDs extracts the TraceID and SpanID from a lightstep context.
func getLightstepSpanIDs(
	lightstep opentracing.Tracer, spanCtx opentracing.SpanContext,
) (traceID uint64, spanID uint64) {
	// Retrieve the trace metadata from lightstep.
	var carrier lightstepExtractIDsCarrier
	if err := lightstep.Inject(spanCtx, opentracing.TextMap, &carrier); err != nil {
		panic(fmt.Sprintf("error injecting lightstep context %s", err))
	}
	if carrier.traceID == 0 || carrier.spanID == 0 {
		panic(fmt.Sprintf("lightstep did not inject IDs: %d, %d", carrier.traceID, carrier.spanID))
	}
	return carrier.traceID, carrier.spanID
}

type recordableOption struct{}

// Recordable is a StartSpanOption that forces creation of a real span.
//
// When tracing is disabled all spans are noopSpans; these spans aren't
// capable of recording, so this option should be passed to StartSpan if the
// caller wants to be able to call StartRecording on the resulting span.
var Recordable opentracing.StartSpanOption = recordableOption{}

func (recordableOption) Apply(*opentracing.StartSpanOptions) {}

// StartSpan is part of the opentracing.Tracer interface.
func (t *Tracer) StartSpan(
	operationName string, opts ...opentracing.StartSpanOption,
) opentracing.Span {
	// Fast paths to avoid the allocation of StartSpanOptions below when tracing
	// is disabled: if we have no options or a single SpanReference (the common
	// case) with a noop context, return a noop span now.
	if len(opts) == 1 {
		if o, ok := opts[0].(opentracing.SpanReference); ok {
			if _, noopCtx := o.ReferencedContext.(noopSpanContext); noopCtx {
				return &t.noopSpan
			}
		}
	}

	netTrace := enableNetTrace.Get()
	lsTr := getLightstep()

	if len(opts) == 0 && !netTrace && lsTr == nil {
		return &t.noopSpan
	}

	var sso opentracing.StartSpanOptions
	var recordable bool
	for _, o := range opts {
		o.Apply(&sso)
		if _, ok := o.(recordableOption); ok {
			recordable = true
		}
	}

	var hasParent bool
	var parentType opentracing.SpanReferenceType
	var parentCtx *spanContext
	var recordingGroup *spanGroup
	var recordingType RecordingType

	for _, r := range sso.References {
		if r.Type != opentracing.ChildOfRef && r.Type != opentracing.FollowsFromRef {
			continue
		}
		if r.ReferencedContext == nil {
			continue
		}
		if _, noopCtx := r.ReferencedContext.(noopSpanContext); noopCtx {
			continue
		}
		hasParent = true
		parentType = r.Type
		parentCtx = r.ReferencedContext.(*spanContext)
		if parentCtx.recordingGroup != nil {
			recordingGroup = parentCtx.recordingGroup
			recordingType = parentCtx.recordingType
		} else if parentCtx.Baggage[Snowball] != "" {
			// Automatically enable recording if we have the Snowball baggage item.
			recordingGroup = new(spanGroup)
			recordingType = SnowballRecording
		}
		// TODO(radu): can we do something for multiple references?
		break
	}
	if hasParent && parentCtx.lightstep == nil {
		// If a lightstep tracer was configured, don't use it if the parent span
		// isn't using it.
		lsTr = nil
	}

	// If tracing is disabled, the Recordable option wasn't passed, and we're not
	// part of a recording or snowball trace, avoid overhead and return a noop
	// span.
	if !recordable && recordingGroup == nil && lsTr == nil && !netTrace {
		return &t.noopSpan
	}

	s := &span{
		tracer:    t,
		operation: operationName,
		startTime: sso.StartTime,
	}
	if s.startTime.IsZero() {
		s.startTime = time.Now()
	}
	s.mu.duration = -1

	// If we are using lightstep, we create a new lightstep span and use the
	// metadata (TraceID, SpanID, Baggage) from that span. Otherwise, we generate
	// our own IDs.
	if lsTr != nil {
		// Create the shadow lightstep span.
		var lsOpts []opentracing.StartSpanOption
		// Replicate the options, using the lightstep context in the reference.
		if !sso.StartTime.IsZero() {
			lsOpts = append(lsOpts, opentracing.StartTime(sso.StartTime))
		}
		if sso.Tags != nil {
			lsOpts = append(lsOpts, opentracing.Tags(sso.Tags))
		}
		if hasParent {
			if parentCtx.lightstep == nil {
				panic("lightstep span derived from non-lightstep span")
			}
			lsOpts = append(lsOpts, opentracing.SpanReference{
				Type:              parentType,
				ReferencedContext: parentCtx.lightstep,
			})
		}
		s.lightstep = lsTr.StartSpan(operationName, lsOpts...)
		s.TraceID, s.SpanID = getLightstepSpanIDs(lsTr, s.lightstep.Context())
		if hasParent && s.TraceID != parentCtx.TraceID {
			panic(fmt.Sprintf(
				"TraceID doesn't match between parent (%d) and child (%d) spans",
				parentCtx.TraceID, s.TraceID,
			))
		}
	} else {
		s.SpanID = uint64(rand.Int63())

		if !hasParent {
			// No parent Span; allocate new trace id.
			s.TraceID = uint64(rand.Int63())
		} else {
			s.TraceID = parentCtx.TraceID
		}
	}

	// Start recording if necessary.
	if recordingGroup != nil {
		s.enableRecording(recordingGroup, recordingType)
	}

	if netTrace {
		s.netTr = trace.New("tracing", operationName)
		s.netTr.SetMaxEvents(maxLogsPerSpan)
	}

	if hasParent {
		s.parentSpanID = parentCtx.SpanID
		// Copy baggage from parent.
		if l := len(parentCtx.Baggage); l > 0 {
			s.mu.Baggage = make(map[string]string, l)
			for k, v := range parentCtx.Baggage {
				s.mu.Baggage[k] = v
			}
		}
	}

	for k, v := range sso.Tags {
		s.SetTag(k, v)
	}

	if netTrace || lsTr != nil {
		// Copy baggage items to tags so they show up in the Lightstep UI or x/net/trace.
		for k, v := range s.mu.Baggage {
			s.SetTag(k, v)
		}
	}

	return s
}

// Inject is part of the opentracing.Tracer interface.
func (t *Tracer) Inject(
	osc opentracing.SpanContext, format interface{}, carrier interface{},
) error {
	if _, noopCtx := osc.(noopSpanContext); noopCtx {
		// Fast path when tracing is disabled. Extract will accept an empty map as a
		// noop context.
		return nil
	}

	// We only support the HTTPHeaders/TextMap format.
	if format != opentracing.HTTPHeaders && format != opentracing.TextMap {
		return opentracing.ErrUnsupportedFormat
	}

	mapWriter, ok := carrier.(opentracing.TextMapWriter)
	if !ok {
		return opentracing.ErrInvalidCarrier
	}

	sc, ok := osc.(*spanContext)
	if !ok {
		return opentracing.ErrInvalidSpanContext
	}

	mapWriter.Set(fieldNameTraceID, strconv.FormatUint(sc.TraceID, 16))
	mapWriter.Set(fieldNameSpanID, strconv.FormatUint(sc.SpanID, 16))
	mapWriter.Set(fieldNameSampled, "true")

	for k, v := range sc.Baggage {
		mapWriter.Set(prefixBaggage+k, v)
	}

	return nil
}

// Extract is part of the opentracing.Tracer interface.
// It always returns a valid context, even in error cases (this is assumed by the
// grpc-opentracing interceptor).
func (t *Tracer) Extract(format interface{}, carrier interface{}) (opentracing.SpanContext, error) {
	// We only support the HTTPHeaders/TextMap format.
	if format != opentracing.HTTPHeaders && format != opentracing.TextMap {
		return noopSpanContext{}, opentracing.ErrUnsupportedFormat
	}

	mapReader, ok := carrier.(opentracing.TextMapReader)
	if !ok {
		return noopSpanContext{}, opentracing.ErrInvalidCarrier
	}

	var sc spanContext

	err := mapReader.ForeachKey(func(k, v string) error {
		switch k = strings.ToLower(k); k {
		case fieldNameTraceID:
			var err error
			sc.TraceID, err = strconv.ParseUint(v, 16, 64)
			if err != nil {
				return opentracing.ErrSpanContextCorrupted
			}
		case fieldNameSpanID:
			var err error
			sc.SpanID, err = strconv.ParseUint(v, 16, 64)
			if err != nil {
				return opentracing.ErrSpanContextCorrupted
			}
		default:
			if strings.HasPrefix(k, prefixBaggage) {
				if sc.Baggage == nil {
					sc.Baggage = make(map[string]string)
				}
				sc.Baggage[strings.TrimPrefix(k, prefixBaggage)] = v
			}
		}
		return nil
	})
	if err != nil {
		return noopSpanContext{}, err
	}
	if sc.TraceID == 0 && sc.SpanID == 0 {
		return noopSpanContext{}, nil
	}

	if lightstep := getLightstep(); lightstep != nil {
		// Extract the lightstep context. For this to work, our key-value "schema"
		// must match lightstep's exactly (otherwise we get an error here).
		sc.lightstep, err = lightstep.Extract(format, carrier)
		if err != nil {
			return noopSpanContext{}, err
		}
	}
	return &sc, nil
}

// FinishSpan closes the given span (if not nil). It is a convenience wrapper
// for span.Finish() which tolerates nil spans.
func FinishSpan(span opentracing.Span) {
	if span != nil {
		span.Finish()
	}
}

// ForkCtxSpan checks if ctx has a Span open; if it does, it creates a new Span
// that follows from the original Span. This allows the resulting context to be
// used in an async task that might outlive the original operation.
//
// Returns the new context and the new span (if any). The span should be
// closed via FinishSpan.
func ForkCtxSpan(ctx context.Context, opName string) (context.Context, opentracing.Span) {
	if span := opentracing.SpanFromContext(ctx); span != nil {
		if IsNoopSpan(span) {
			// Optimization: avoid ContextWithSpan call if tracing is disabled.
			return ctx, span
		}
		tr := span.Tracer()
		newSpan := tr.StartSpan(opName, opentracing.FollowsFrom(span.Context()))
		return opentracing.ContextWithSpan(ctx, newSpan), newSpan
	}
	return ctx, nil
}

// ChildSpan opens a span as a child of the current span in the context (if
// there is one).
//
// Returns the new context and the new span (if any). The span should be
// closed via FinishSpan.
func ChildSpan(ctx context.Context, opName string) (context.Context, opentracing.Span) {
	span := opentracing.SpanFromContext(ctx)
	if span == nil {
		return ctx, nil
	}
	if IsNoopSpan(span) {
		// Optimization: avoid ContextWithSpan call if tracing is disabled.
		return ctx, span
	}
	newSpan := span.Tracer().StartSpan(opName, opentracing.ChildOf(span.Context()))
	return opentracing.ContextWithSpan(ctx, newSpan), newSpan
}

// EnsureContext checks whether the given context.Context contains a Span. If
// not, it creates one using the provided Tracer and wraps it in the returned
// Span. The returned closure must be called after the request has been fully
// processed.
func EnsureContext(
	ctx context.Context, tracer opentracing.Tracer, name string,
) (context.Context, func()) {
	if opentracing.SpanFromContext(ctx) == nil {
		sp := tracer.StartSpan(name)
		return opentracing.ContextWithSpan(ctx, sp), sp.Finish
	}
	return ctx, func() {}
}

// StartSnowballTrace takes in a context and returns a derived one with a
// "snowball span" in it. The caller takes ownership of this span from the
// returned context and is in charge of Finish()ing it. The span has recording
// enabled.
//
// TODO(andrei): remove this method once EXPLAIN(TRACE) is gone.
func StartSnowballTrace(
	ctx context.Context, tracer opentracing.Tracer, opName string,
) (context.Context, opentracing.Span, error) {
	var span opentracing.Span
	if parentSpan := opentracing.SpanFromContext(ctx); parentSpan != nil {
		span = parentSpan.Tracer().StartSpan(
			opName, opentracing.ChildOf(parentSpan.Context()), Recordable,
		)
	} else {
		span = tracer.StartSpan(opName, Recordable)
	}
	StartRecording(span, SnowballRecording)
	return opentracing.ContextWithSpan(ctx, span), span, nil
}
