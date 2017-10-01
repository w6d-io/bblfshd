package daemon

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/runc/libcontainer"
	"github.com/stretchr/testify/require"
	"gopkg.in/bblfsh/sdk.v1/protocol"
)

type mockDriver struct {
	CalledClose int
	MockStatus  libcontainer.Status
}

func newMockDriver() (Driver, error) {
	return &mockDriver{
		MockStatus: libcontainer.Running,
	}, nil
}

func (d *mockDriver) Service() protocol.ProtocolServiceClient {
	return nil
}

func (d *mockDriver) Start() error {
	return nil
}

func (d *mockDriver) Status() (libcontainer.Status, error) {
	return d.MockStatus, nil
}

func (d *mockDriver) Stop() error {
	d.CalledClose++
	return nil
}

func TestNewDriverPool_StartNoopClose(t *testing.T) {
	require := require.New(t)
	dp := NewDriverPool(newMockDriver)

	err := dp.Start()
	require.NoError(err)

	err = dp.Stop()
	require.NoError(err)

	err = dp.Stop()
	require.True(ErrPoolClosed.Is(err))

	err = dp.Execute(nil)
	require.True(ErrPoolClosed.Is(err))
}

func TestNewDiverPool_StartFailingDriver(t *testing.T) {
	require := require.New(t)

	dp := NewDriverPool(func() (Driver, error) {
		return nil, fmt.Errorf("driver error")
	})

	err := dp.Start()
	require.EqualError(err, "driver error")
}

func TestNewDriverPool_Recovery(t *testing.T) {
	require := require.New(t)

	var called int
	dp := NewDriverPool(func() (Driver, error) {
		called++
		return newMockDriver()
	})

	err := dp.Start()
	require.NoError(err)

	for i := 0; i < 100; i++ {
		err := dp.Execute(func(d Driver) error {
			require.NotNil(d)

			if i%10 == 0 {
				d.(*mockDriver).MockStatus = libcontainer.Stopped
			}

			return nil
		})

		require.Nil(err)
		require.Equal(dp.instances.Value(), 1)
	}

	err = dp.Stop()
	require.NoError(err)

	require.Equal(called, 11)
}

func TestNewDriverPool_Sequential(t *testing.T) {
	require := require.New(t)

	dp := NewDriverPool(newMockDriver)

	err := dp.Start()
	require.NoError(err)

	for i := 0; i < 100; i++ {
		err := dp.Execute(func(d Driver) error {
			require.NotNil(d)
			return nil
		})

		require.Nil(err)
		require.Equal(dp.instances.Value(), 1)
	}

	err = dp.Stop()
	require.NoError(err)
}

func TestNewDriverPool_Parallel(t *testing.T) {
	require := require.New(t)

	dp := NewDriverPool(newMockDriver)

	err := dp.Start()
	require.NoError(err)

	var wg sync.WaitGroup
	wg.Add(100)
	for i := 0; i < 100; i++ {
		go func() {
			err := dp.Execute(func(Driver) error {
				defer wg.Done()
				time.Sleep(50 * time.Millisecond)
				return nil
			})

			require.Nil(err)
			require.True(dp.instances.Value() >= 1)
		}()
	}

	wg.Wait()
	require.Equal(runtime.NumCPU(), dp.instances.Value())

	time.Sleep(time.Second)
	require.Equal(1, dp.instances.Value())

	err = dp.Stop()
	require.NoError(err)
}

type mockScalingPolicy struct {
	Total, Load int
	Result      int
}

func (p *mockScalingPolicy) Scale(total int, load int) int {
	p.Total = total
	p.Load = load
	return p.Result
}

func TestMinMax(t *testing.T) {
	require := require.New(t)

	m := &mockScalingPolicy{}
	p := MinMax(5, 10, m)
	m.Result = 1
	require.Equal(5, p.Scale(1, 1))
	m.Result = 5
	require.Equal(5, p.Scale(1, 1))
	m.Result = 7
	require.Equal(7, p.Scale(1, 1))
	m.Result = 10
	require.Equal(10, p.Scale(1, 1))
	m.Result = 11
	require.Equal(10, p.Scale(1, 1))
}

func TestMovingAverage(t *testing.T) {
	require := require.New(t)

	m := &mockScalingPolicy{}
	p := MovingAverage(1, m)
	p.Scale(1, 2)
	require.Equal(1, m.Total)
	require.Equal(2, m.Load)
	p.Scale(1, 50)
	require.Equal(1, m.Total)
	require.Equal(50, m.Load)

	p = MovingAverage(2, m)
	p.Scale(1, 1)
	require.Equal(1, m.Load)
	p.Scale(1, 3)
	require.Equal(2, m.Load)
	p.Scale(1, 7)
	require.Equal(5, m.Load)

	p = MovingAverage(100, m)
	for i := 0; i < 100; i++ {
		p.Scale(1, 200)
		require.Equal(200, m.Load)
	}

	for i := 0; i < 50; i++ {
		p.Scale(1, 100)
	}
	require.Equal(150, m.Load)
}

func TestAIMD(t *testing.T) {
	require := require.New(t)

	p := AIMD(1, 0.5)

	require.Equal(0, p.Scale(0, 0))
	require.Equal(1, p.Scale(1, 0))

	require.Equal(1, p.Scale(0, 1))
	require.Equal(2, p.Scale(1, 1))

	require.Equal(0, p.Scale(1, -1))
	require.Equal(1, p.Scale(2, -1))
}
