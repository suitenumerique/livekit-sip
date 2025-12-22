package sip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst/app"
)

type MediaInput = MediaLink

func NewMediaInput(ctx context.Context, dst *app.Source, src io.ReadCloser) (*MediaInput, error) {
	w, err := NewGstWriter(dst)
	if err != nil {
		return nil, fmt.Errorf("failed to create GStreamer writer: %w", err)
	}
	link := NewMediaLink(ctx, w, src)
	return link, nil
}

type MediaOutput = MediaLink

func NewMediaOutput(ctx context.Context, dst io.WriteCloser, src *app.Sink) (*MediaOutput, error) {
	r, err := NewGstReader(src)
	if err != nil {
		return nil, fmt.Errorf("failed to create GStreamer reader: %w", err)
	}
	link := NewMediaLink(ctx, dst, r)
	return link, nil
}

type MediaIO struct {
	mu sync.Mutex

	Inputs  []*MediaInput
	Outputs []*MediaOutput

	ctx    context.Context
	cancel context.CancelFunc

	timeout time.Duration

	running bool
}

func NewMediaIO(ctx context.Context, timeout time.Duration) *MediaIO {
	ctx, cancel := context.WithCancel(ctx)
	return &MediaIO{
		ctx:     ctx,
		cancel:  cancel,
		Inputs:  make([]*MediaInput, 0),
		Outputs: make([]*MediaOutput, 0),
		timeout: timeout,
	}
}

func (m *MediaIO) AddInputs(inputs ...*MediaInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error

	wg := sync.WaitGroup{}
	for _, in := range inputs {
		wg.Add(1)
		go func(in *MediaInput) {
			defer wg.Done()
			if m.running {
				if err := in.Start(); err != nil {
					errs = append(errs, err)
				}
			}
		}(in)
	}
	wg.Wait()
	m.Inputs = append(m.Inputs, inputs...)

	if len(errs) > 0 {
		return fmt.Errorf("failed to add media input: %v", errs)
	}
	return nil
}

func (m *MediaIO) AddOutputs(outputs ...*MediaOutput) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error

	wg := sync.WaitGroup{}
	for _, out := range outputs {
		wg.Add(1)
		go func(out *MediaOutput) {
			defer wg.Done()
			if m.running {
				if err := out.Start(); err != nil {
					errs = append(errs, err)
				}
			}
		}(out)
	}
	wg.Wait()
	m.Outputs = append(m.Outputs, outputs...)

	if len(errs) > 0 {
		return fmt.Errorf("failed to add media output: %v", errs)
	}
	return nil
}

func (m *MediaIO) RemoveInput(input *MediaInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !slices.Contains(m.Inputs, input) {
		return errors.New("media input not found")
	}

	m.Inputs = slices.Delete(m.Inputs, slices.Index(m.Inputs, input), 1)
	if m.running {
		if err := input.Close(); err != nil {
			return fmt.Errorf("failed to stop media input: %w", err)
		}
	}
	return nil
}

func (m *MediaIO) RemoveOutput(output *MediaOutput) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !slices.Contains(m.Outputs, output) {
		return errors.New("media output not found")
	}

	m.Outputs = slices.Delete(m.Outputs, slices.Index(m.Outputs, output), 1)
	if m.running {
		if err := output.Close(); err != nil {
			return fmt.Errorf("failed to stop media output: %w", err)
		}
	}
	return nil
}

func (m *MediaIO) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return nil
	}

	var errs []error

	wg := sync.WaitGroup{}
	for _, in := range m.Inputs {
		wg.Add(1)
		go func(in *MediaInput) {
			defer wg.Done()
			if err := in.Start(); err != nil {
				errs = append(errs, err)
			}
		}(in)
	}
	for _, out := range m.Outputs {
		wg.Add(1)
		go func(out *MediaOutput) {
			defer wg.Done()
			if err := out.Start(); err != nil {
				errs = append(errs, err)
			}
		}(out)
	}
	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("failed to start media IO: %v", errs)
	}

	m.running = true
	return nil
}

func (m *MediaIO) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return nil
	}

	var errs []error

	fmt.Printf("Closing %d outputs\n", len(m.Outputs))
	wg := sync.WaitGroup{}
	for _, out := range m.Outputs {
		wg.Add(1)
		go func(out *MediaOutput) {
			defer wg.Done()
			if err := out.Close(); err != nil {
				errs = append(errs, err)
			}
			fmt.Printf("Closed output\n")
		}(out)
	}
	wg.Wait()
	m.Outputs = []*MediaOutput{}
	fmt.Printf("Closing %d inputs\n", len(m.Inputs))
	wg = sync.WaitGroup{}
	for _, in := range m.Inputs {
		wg.Add(1)
		go func(in *MediaInput) {
			defer wg.Done()
			if err := in.Close(); err != nil {
				errs = append(errs, err)
			}
			fmt.Printf("Closed input\n")
		}(in)
	}
	wg.Wait()
	m.Inputs = []*MediaInput{}
	fmt.Printf("All inputs and outputs closed\n")
	if len(errs) > 0 {
		return fmt.Errorf("failed to stop media IO: %v", errs)
	}

	m.running = false
	return nil
}
