package main

import "context"

// startCombineTask keeps the one memory-intensive FFmpeg combine independent
// from projectSem. That leaves the project workers available for scene
// submission, status work, and streaming downloads while a final video is
// being encoded.
func (p *Processor) startCombineTask(ctx context.Context, key string, task func(context.Context)) {
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	select {
	case p.combineSem <- struct{}{}:
	default:
		return
	}
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		<-p.combineSem
		return
	}
	p.inFlight[key] = struct{}{}
	p.mu.Unlock()
	go func() {
		defer func() {
			<-p.combineSem
			p.mu.Lock()
			delete(p.inFlight, key)
			p.mu.Unlock()
		}()
		task(ctx)
	}()
}

func (p *Processor) startProjectTask(ctx context.Context, key string, task func(context.Context)) {
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	select {
	case p.projectSem <- struct{}{}:
	default:
		return
	}
	p.mu.Lock()
	if _, exists := p.inFlight[key]; exists {
		p.mu.Unlock()
		<-p.projectSem
		return
	}
	p.inFlight[key] = struct{}{}
	p.mu.Unlock()
	go func() {
		defer func() {
			<-p.projectSem
			p.mu.Lock()
			delete(p.inFlight, key)
			p.mu.Unlock()
		}()
		task(ctx)
	}()
}
