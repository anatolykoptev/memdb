package metrics

// TrackCall — стандартный паттерн «счётчик вызовов + счётчик ошибок».
// Эквивалентно:
//
//	if reg != nil { reg.Incr(callName) }
//	err := fn()
//	if err != nil && reg != nil { reg.Incr(errName) }
//	return err
//
// Nil-safe: при reg == nil просто возвращает fn() без инкрементов.
func TrackCall(reg *Registry, callName, errName string, fn func() error) error {
	if reg == nil {
		return fn()
	}
	return reg.TrackOperation(callName, errName, fn)
}

// TrackCallTimed — TrackCall + замер длительности через StartTimer.
// latencyName — имя метрики для таймера (рекомендуется суффикс _seconds).
func TrackCallTimed(reg *Registry, callName, errName, latencyName string, fn func() error) error {
	if reg == nil {
		return fn()
	}
	defer reg.StartTimer(latencyName).Stop()
	return reg.TrackOperation(callName, errName, fn)
}
