package sipserver

import (
	"fmt"

	"mcs/internal/store"
)

func (s *Server) PagingInfo() map[string]any {
	return map[string]any{
		"enabled":      s.pagingCfg.Enabled,
		"pin_required": s.pagingCfg.PIN != "",
	}
}

func (s *Server) PagingEvents(limit int) []store.PagingEvent {
	if s.store == nil {
		return nil
	}
	return s.store.ListPagingEvents(limit)
}

func (s *Server) PagingStop(id string) error {
	p := s.pagingGet(id)
	if p == nil {
		return fmt.Errorf("寻呼不存在或已结束")
	}
	p.stop()
	return nil
}
