package main

import (
	"context"
	"fmt"
	"math/rand"
	"path"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"cirello.io/dynamolock"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/davecgh/go-spew/spew"
	"github.com/rbranson/nbmutex"
	"github.com/rbranson/porcupine"
)

const concurrency = 100
const dynamoTable = "Locks"
const outputVisualization = false
const vizOutputDir = "viz"
const dynamolockLeaseDuration = 1 * time.Second
const dynamolockHeartbeatPeriod = 100 * time.Millisecond
const maxHeartbeats = 3

type monotonic struct {
	v int64
}

func (m *monotonic) Next() int64 {
	return atomic.AddInt64(&m.v, 1)
}

type lock interface {
	EnsureSetup(context.Context) error
	Acquire(context.Context) (bool, error)
	Release(context.Context) (bool, error)
}

type nolock struct{}

func (n *nolock) EnsureSetup(ctx context.Context) error {
	return nil
}

func (n *nolock) Acquire(ctx context.Context) (bool, error) {
	return true, nil
}

func (n *nolock) Release(ctx context.Context) (bool, error) {
	return true, nil
}

type memlock struct {
	Mu *nbmutex.Mutex
	u  func()
}

func (m *memlock) EnsureSetup(ctx context.Context) error {
	return nil
}

func (m *memlock) Acquire(ctx context.Context) (bool, error) {
	for {
		u, ok := m.Mu.TryLock()
		if ok {
			m.u = u
			return true, nil
		}

		select {
		case <-time.After(10 * time.Microsecond):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func (m *memlock) Release(ctx context.Context) (bool, error) {
	if m.u == nil {
		return false, nil
	}
	m.u()
	m.u = nil
	return true, nil
}

type distlock struct {
	DDB             *dynamodb.DynamoDB
	TableName       string
	Key             string
	LeaseDuration   time.Duration
	HeartbeatPeriod time.Duration

	client *dynamolock.Client
	lock   *dynamolock.Lock
}

func (d *distlock) init() (err error) {
	if d.client != nil {
		return nil
	}
	d.client, err = dynamolock.New(
		NewDynamoDBMock(d.DDB, maxHeartbeats),
		d.TableName,
		dynamolock.WithLeaseDuration(d.LeaseDuration),
		dynamolock.WithHeartbeatPeriod(d.HeartbeatPeriod),
	)
	return
}

func (d *distlock) EnsureSetup(ctx context.Context) error {
	if err := d.init(); err != nil {
		return err
	}
	fmt.Printf("creating table...\n")
	_, err := d.client.CreateTableWithContext(ctx, d.TableName)
	if err != nil {
		awsErr, ok := err.(awserr.Error)
		switch {
		case ok && awsErr.Code() == dynamodb.ErrCodeResourceInUseException:
			// nothing
		default:
			return err
		}
	}
	fmt.Printf("created table.\n")
	return nil
}

func (d *distlock) Acquire(ctx context.Context) (ok bool, err error) {
	if err = d.init(); err != nil {
		return false, err
	}
	d.lock, err = d.client.AcquireLockWithContext(ctx, d.Key, dynamolock.WithAdditionalTimeToWaitForLock(1*time.Second), dynamolock.WithSessionMonitor(200*time.Millisecond, func() {
		fmt.Printf("call Kenny Loggins cause you're in the DANGER ZONE\n")
	}))
	if err != nil {
		return false, err
	}
	ok = true
	return
}

func (d *distlock) Release(ctx context.Context) (ok bool, err error) {
	if d.client == nil || d.lock == nil {
		return
	}
	ok, err = d.client.ReleaseLockWithContext(ctx, d.lock)
	return
}

type registerInput struct {
	op  bool
	val int
}

type eventRecorder struct {
	Events  []porcupine.Event
	Verbose bool
	seq     monotonic
	mu      sync.Mutex
}

func (r *eventRecorder) Append(event porcupine.Event) {
	r.mu.Lock()
	r.Events = append(r.Events, event)
	r.mu.Unlock()

	if r.Verbose {
		spew.Dump(event)
	}
}

type eventSession struct {
	rec      *eventRecorder
	id       int
	clientID int
}

func (r *eventRecorder) Session(clientID int) eventSession {
	return eventSession{
		rec:      r,
		id:       int(r.seq.Next()),
		clientID: clientID,
	}
}

func (r *eventSession) PrePut(val int) {
	r.rec.Append(porcupine.Event{
		ClientId: r.clientID,
		Kind:     porcupine.CallEvent,
		Value: registerInput{
			op:  true,
			val: val,
		},
		Id: r.id,
	})
}

func (r *eventSession) PostPut() {
	r.rec.Append(porcupine.Event{
		ClientId: r.clientID,
		Kind:     porcupine.ReturnEvent,
		Value:    0,
		Id:       r.id,
	})
}

func (r *eventSession) PreGet() {
	r.rec.Append(porcupine.Event{
		ClientId: r.clientID,
		Kind:     porcupine.CallEvent,
		Value: registerInput{
			op: false,
		},
		Id: r.id,
	})
}

func (r *eventSession) PostGet(val int) {
	r.rec.Append(porcupine.Event{
		ClientId: r.clientID,
		Kind:     porcupine.ReturnEvent,
		Value:    val,
		Id:       r.id,
	})
}

// this is specifically designed to replicate a non-linearizable
// shared replicated store which serves reads from replicas
type shared struct {
	Stores []atomic.Value
}

func (s *shared) sleep() {
	micros := rand.Intn(100)
	time.Sleep(time.Duration(micros) * time.Microsecond)
}

func (s *shared) Get() int {
	s.sleep()
	which := rand.Intn(len(s.Stores))
	v, _ := s.Stores[which].Load().(int)
	return v
}

func (s *shared) Put(v int) {
	for i := range s.Stores {
		store := &s.Stores[i] // by-ref
		store.Store(v)
		s.sleep()
	}
}

type actor struct {
	Lock lock
	Func func(ctx context.Context, actorID int) error
	ID int
}

func (a *actor) Run(ctx context.Context) error {
	for {
		select {
		case <-time.After(1 * time.Nanosecond):
			// scheduler yield-ish
		case <-ctx.Done():
			return ctx.Err()
		}

		err := func() error {
			if ok, err := a.Lock.Acquire(ctx); err != nil {
				return err
			} else if !ok {
				return nil
			}
			defer a.Lock.Release(ctx)

			if err := a.Func(ctx, a.ID); err != nil {
				return err
			}

			return nil
		}()

		if err != nil {
			return err
		}
	}
}

func flipcoin() bool {
	if rand.Intn(2) == 1 {
		return true
	}
	return false
}

type contention struct {
	Rec *eventRecorder
	Val *shared
}

func (c *contention) F(ctx context.Context, actorID int) error {
	sesh := c.Rec.Session(actorID)

	if flipcoin() {
		sesh.PreGet()
		v := c.Val.Get()
		sesh.PostGet(v)
	} else {
		v := rand.Int()
		sesh.PrePut(v)
		c.Val.Put(v)
		sesh.PostPut()
	}

	time.Sleep(600*time.Millisecond)
	return nil
}

func checkEvents(events []porcupine.Event) (porcupine.CheckResult, porcupine.Model, interface{}) {
	model := porcupine.Model{
		Init: func() interface{} {
			return 0
		},
		Step: func(state, input, output interface{}) (bool, interface{}) {
			st := state.(int)
			in := input.(registerInput)
			out := output.(int)

			if in.op {
				// put
				return true, in.val
			}

			// get
			return out == st, st
		},
		DescribeOperation: func(input interface{}, output interface{}) string {
			in := input.(registerInput)
			out := output.(int)
			op := "get"

			if in.op {
				op = "put"
			}

			return fmt.Sprintf("%s(%v) -> %v", op, in.val, out)
		},
	}
	cr, info := porcupine.CheckEventsVerbose(model, events, 1*time.Hour)
	return cr, model, info
}

func newLocalDynamoDB() *dynamodb.DynamoDB {
	cfg := aws.Config{
		Endpoint:    aws.String("http://localhost:8000"),
		Region:      aws.String("us-west-2"),
		Credentials: credentials.NewStaticCredentials("foo", "foo", "bar"),
	}
	sess := session.Must(session.NewSession())
	ddb := dynamodb.New(sess, &cfg)
	return ddb
}

type lockFactory func() lock

func checkLock(runfor time.Duration, lf lockFactory) {
	ctx, cancel := context.WithTimeout(context.Background(), runfor)
	defer cancel()

	if err := lf().EnsureSetup(ctx); err != nil {
		fmt.Printf("error ensuring setup: %v\n", err)
		return
	}

	rec := eventRecorder{}
	val := shared{Stores: make([]atomic.Value, 3)}
	cont := contention{
		Rec: &rec,
		Val: &val,
	}

	seq := monotonic{}
	wg := sync.WaitGroup{}
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// make sure each one of these gets it's own thread
			runtime.LockOSThread()

			a := actor{
				Lock: lf(),
				Func: cont.F,
				ID: int(seq.Next()),
			}
			err := a.Run(ctx)
			if err != nil && err != context.DeadlineExceeded {
				fmt.Printf("actor error: %v\n", err)
			}
		}()
	}

	<-ctx.Done()
	wg.Wait()

	fmt.Printf("event count = %d\n", len(rec.Events))
	fmt.Printf("performing model check... (can take a while)\n")

	isLinear, model, info := checkEvents(rec.Events)
	fmt.Printf("linearizable? %v\n", isLinear)

	if outputVisualization {
		filename := fmt.Sprintf("%s.html", time.Now().Format(time.RFC3339Nano))
		filepath := path.Join(vizOutputDir, filename)
		porcupine.VisualizePath(model, info.(porcupine.LinearizationInfo), filepath)
		fmt.Printf("linearizable visualization file: %v\n", filepath)
	}

}

func main() {
	// check memlock
	fmt.Printf("checking memlock... (control, always legal)\n")
	var nbmu nbmutex.Mutex
	checkLock(5*time.Second, func() lock {
		return &memlock{Mu: &nbmu}
	})

	// check no lock
	fmt.Printf("checking no lock... (control, always illegal)\n")
	checkLock(25*time.Millisecond, func() lock {
		return &nolock{}
	})

	// check dynamodb distlock
	fmt.Printf("checking dynamodb lock...\n")
	ddb := newLocalDynamoDB()
	checkLock(10*time.Second, func() lock {
		return &distlock{
			DDB:             ddb,
			TableName:       dynamoTable,
			Key:             "lockcheck",
			LeaseDuration:   dynamolockLeaseDuration,
			HeartbeatPeriod: dynamolockHeartbeatPeriod,
		}
	})
}
