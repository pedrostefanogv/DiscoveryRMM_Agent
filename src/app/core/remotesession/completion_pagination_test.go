//go:build windows

package remotesession

import (
	"math"
	"testing"
)

// ── Paginação: page vem do viewer e não pode estourar int ──

// page*pageSize com page enorme estourava int e virava um índice NEGATIVO,
// causando panic de slice dentro do handler NATS (o processo do agent caía).
func TestPaginateCompletion_PageEnormeNaoEntraEmPanic(t *testing.T) {
	result := tabExpansionResult{Matches: []completionMatch{{Text: "a"}, {Text: "b"}}}
	casos := []struct {
		nome string
		page int
		size int
	}{
		{"maxint", math.MaxInt, completionMaxItems},
		{"metade de maxint", math.MaxInt / 2, completionMaxItems},
		{"estouro com pageSize 1", math.MaxInt, 1},
	}
	for _, c := range casos {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s: paginateCompletion entrou em panic: %v", c.nome, r)
				}
			}()
			res := paginateCompletion(completionReq{Page: c.page, PageSize: c.size}, result)
			if res.Page != c.page {
				t.Fatalf("%s: Page = %d, want %d", c.nome, res.Page, c.page)
			}
			if len(res.Matches) != 0 {
				t.Fatalf("%s: deveria vir vazio (page além do fim); obtidos %d itens", c.nome, len(res.Matches))
			}
			if res.HasMore {
				t.Fatalf("%s: HasMore deveria ser false", c.nome)
			}
			if res.TotalCount != 2 {
				t.Fatalf("%s: TotalCount = %d, want 2", c.nome, res.TotalCount)
			}
		}()
	}
}

func TestPaginateCompletion_PageNegativaViraZero(t *testing.T) {
	result := tabExpansionResult{Matches: []completionMatch{{Text: "a"}, {Text: "b"}, {Text: "c"}}}
	res := paginateCompletion(completionReq{Page: -7, PageSize: 2}, result)
	if res.Page != 0 || len(res.Matches) != 2 || !res.HasMore {
		t.Fatalf("page negativa: %+v", res)
	}
}

func TestPaginateCompletion_SegundaPagina(t *testing.T) {
	result := tabExpansionResult{Matches: []completionMatch{{Text: "a"}, {Text: "b"}, {Text: "c"}}}
	res := paginateCompletion(completionReq{Page: 1, PageSize: 2}, result)
	if len(res.Matches) != 1 || res.Matches[0].Text != "c" || res.HasMore {
		t.Fatalf("segunda pagina: %+v", res)
	}
}

// Cobertura do caminho real (JSON do viewer -> handler -> resposta): o panic
// anterior derrubava o processo, não só o pedido.
func TestBuildCompletionResponse_PageEnormeNaoDerruba(t *testing.T) {
	anterior := defaultCompletionRegistry
	defaultCompletionRegistry = &completionRegistry{providers: []completionProvider{muitosProvider{}}}
	t.Cleanup(func() { defaultCompletionRegistry = anterior })

	st := &SessionTerminal{sessionID: "sess-page", termShellKind: "cmd", termBackend: "conpty"}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("buildCompletionResponse entrou em panic: %v", r)
		}
	}()
	res := decodeRes(t, st.buildCompletionResponse([]byte(
		`{"reqId":"r1","input":"x","cursor":1,"page":9223372036854775807,"pageSize":50}`)))
	if !res.Ok {
		t.Fatalf("resposta deveria ser ok=true; got %+v", res)
	}
	if len(res.Matches) != 0 {
		t.Fatalf("page além do fim deveria vir vazia; obtidos %d", len(res.Matches))
	}
}
