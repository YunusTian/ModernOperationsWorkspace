package main

import "testing"

func TestAIChatOpenRejectsEmptyMessages(t *testing.T) {
	a := &App{}
	if _, err := a.AIChatOpen(AIChatOpenInput{}); err == nil {
		t.Fatal("expected empty messages error")
	}
}

func TestAIChatStartRejectsUnknownSession(t *testing.T) {
	a := &App{}
	if err := a.AIChatStart("missing"); err == nil {
		t.Fatal("expected missing session error")
	}
}
