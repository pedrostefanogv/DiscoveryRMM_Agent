package automation

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Extração de canais numéricos do stdout de sensores de SCRIPT.
//
// Duas fontes, aplicadas nesta ordem:
//  1. Protocolo MDZ (o mesmo das automações): o script declara status, mensagem
//     e métricas. As linhas de protocolo são CONSUMIDAS (não fazem parte do
//     output exibido/armazenado) e "último valor vence".
//  2. Extratores configurados no sensor (ExtractorsJson): regex, json ou mdz.
//
// Nenhum status/severidade é calculado aqui: o agente só extrai NÚMEROS. Quem
// decide é o servidor (SensorEvaluator.Evaluate no API).
//
// Canal que não está declarado em Channels é DESCARTADO: sem esse filtro, um
// script que imprime JSON com dezenas de números explodiria a cardinalidade
// dos gráficos.
// ─────────────────────────────────────────────────────────────────────────────

const (
	mdzStatusPrefix     = "MDZ_STATUS:"
	mdzMessagePrefix    = "MDZ_MESSAGE:"
	mdzMetricPrefix     = "MDZ_METRIC:"
	mdzMetricJSONPrefix = "MDZ_METRIC_JSON:"
)

// sensorParsedOutput é o resultado do parsing do stdout de um sensor.
type sensorParsedOutput struct {
	// Output é o stdout SEM as linhas de protocolo MDZ.
	Output   string
	Status   string // OK|WARNING|CRITICAL|UNKNOWN (último MDZ_STATUS)
	Message  string // MDZ_MESSAGE (último)
	Channels map[string]float64
}

// ── OptionsJson ──

// sensorOptions é o OptionsJson do sensor já decodificado. As opções são
// específicas do TIPO (ex.: count do ping, expectedStatusMin do HttpProbe,
// script inline do Script) e são lidas por nome com aliases.
type sensorOptions struct {
	raw map[string]json.RawMessage
}

// parseSensorOptions decodifica o OptionsJson. JSON inválido/vazio vira um
// conjunto vazio de opções (o coletor aplica os defaults) — opção corrompida
// não pode derrubar a coleta do sensor.
func parseSensorOptions(raw string) sensorOptions {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return sensorOptions{}
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return sensorOptions{}
	}
	normalized := make(map[string]json.RawMessage, len(parsed))
	for key, value := range parsed {
		normalized[strings.ToLower(strings.TrimSpace(key))] = value
	}
	return sensorOptions{raw: normalized}
}

func (o sensorOptions) value(names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if raw, ok := o.raw[strings.ToLower(strings.TrimSpace(name))]; ok {
			return raw, true
		}
	}
	return nil, false
}

// String devolve a primeira opção textual presente (case-insensitive).
func (o sensorOptions) String(names ...string) string {
	raw, ok := o.value(names...)
	if !ok {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

// Int devolve a primeira opção numérica presente; fallback quando ausente.
func (o sensorOptions) Int(fallback int, names ...string) int {
	raw, ok := o.value(names...)
	if !ok {
		return fallback
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err != nil {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return fallback
		}
		parsed, ok := parseLooseFloat(text)
		if !ok {
			return fallback
		}
		return int(parsed)
	}
	return int(number)
}

// Bool devolve a primeira opção booleana presente; fallback quando ausente.
func (o sensorOptions) Bool(fallback bool, names ...string) bool {
	raw, ok := o.value(names...)
	if !ok {
		return fallback
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return fallback
}

// Strings devolve a primeira opção de LISTA DE TEXTOS presente. Aceita array
// JSON (["-Algo","valor"]) e, por tolerância, uma string única (que vira uma
// lista de 1 item — valores de env podem conter espaço, então NÃO dividimos).
// Usado pelas opções arguments/envVars do sensor Script.
func (o sensorOptions) Strings(names ...string) []string {
	raw, ok := o.value(names...)
	if !ok {
		return nil
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err == nil {
		return cleanSensorStringList(items)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return cleanSensorStringList([]string{text})
	}
	// Array misto (número/booleano junto de string): converte o que der.
	var generic []any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil
	}
	converted := make([]string, 0, len(generic))
	for _, item := range generic {
		switch typed := item.(type) {
		case string:
			converted = append(converted, typed)
		case float64:
			converted = append(converted, strconv.FormatFloat(typed, 'f', -1, 64))
		case bool:
			converted = append(converted, strconv.FormatBool(typed))
		}
	}
	return cleanSensorStringList(converted)
}

// cleanSensorStringList remove itens vazios (espaços) sem reordenar.
func cleanSensorStringList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ── ExtractorsJson ──

// sensorExtractor é uma regra de extração de UM canal numérico.
type sensorExtractor struct {
	// Type: regex | json | mdz.
	Type    string
	Channel string
	Regex   string
	// JSONPath é um dot path ("download.bandwidth").
	JSONPath string
	// Scale multiplica o valor encontrado (ex.: bytes → Mbps usa 8e-6).
	Scale float64
	// Aggregate: last (default) | first | min | max | avg | sum.
	Aggregate string

	compiled *regexp.Regexp
}

// sensorExtractorWire é o formato JSON aceito em ExtractorsJson. Aceitamos
// "jsonPath" e "path" porque o editor do console usa os dois nomes
// historicamente.
type sensorExtractorWire struct {
	Type      string   `json:"type"`
	Channel   string   `json:"channel"`
	Regex     string   `json:"regex"`
	JSONPath  string   `json:"jsonPath"`
	Path      string   `json:"path"`
	Scale     *float64 `json:"scale"`
	Aggregate string   `json:"aggregate"`
}

// parseSensorExtractors decodifica o ExtractorsJson. Regra inválida (sem canal,
// regex que não compila) é IGNORADA: uma regra quebrada no console não pode
// impedir os outros canais do mesmo sensor.
func parseSensorExtractors(raw string) []sensorExtractor {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return nil
	}
	var wire []sensorExtractorWire
	if err := json.Unmarshal([]byte(trimmed), &wire); err != nil {
		return nil
	}
	out := make([]sensorExtractor, 0, len(wire))
	for _, item := range wire {
		channel := strings.TrimSpace(item.Channel)
		if channel == "" {
			continue
		}
		extractor := sensorExtractor{
			Type:      strings.ToLower(strings.TrimSpace(item.Type)),
			Channel:   channel,
			Regex:     strings.TrimSpace(item.Regex),
			JSONPath:  strings.TrimSpace(item.JSONPath),
			Scale:     1,
			Aggregate: strings.ToLower(strings.TrimSpace(item.Aggregate)),
		}
		if extractor.JSONPath == "" {
			extractor.JSONPath = strings.TrimSpace(item.Path)
		}
		if item.Scale != nil && *item.Scale != 0 {
			extractor.Scale = *item.Scale
		}
		if extractor.Type == "" {
			switch {
			case extractor.Regex != "":
				extractor.Type = "regex"
			case extractor.JSONPath != "":
				extractor.Type = "json"
			default:
				extractor.Type = "mdz"
			}
		}
		if extractor.Aggregate == "" {
			extractor.Aggregate = "last"
		}
		if extractor.Type == "regex" {
			if extractor.Regex == "" {
				continue
			}
			compiled, err := regexp.Compile(extractor.Regex)
			if err != nil {
				continue
			}
			extractor.compiled = compiled
		}
		if extractor.Type == "json" && extractor.JSONPath == "" {
			continue
		}
		out = append(out, extractor)
	}
	return out
}

// ── Parser ──

// parseSensorStdout consome o protocolo MDZ do stdout e aplica os extratores.
// declared = canais declarados pelo sensor (filtro); vazio = aceita qualquer um.
func parseSensorStdout(stdout string, declared map[string]bool, extractors []sensorExtractor) sensorParsedOutput {
	result := sensorParsedOutput{Channels: make(map[string]float64)}
	metrics := make(map[string]float64)

	lines := strings.Split(stdout, "\n")
	kept := make([]string, 0, len(lines))
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		switch {
		case strings.HasPrefix(upper, mdzStatusPrefix):
			if value := strings.TrimSpace(trimmed[len(mdzStatusPrefix):]); value != "" {
				result.Status = strings.ToUpper(value)
			}
			continue
		case strings.HasPrefix(upper, mdzMessagePrefix):
			if value := strings.TrimSpace(trimmed[len(mdzMessagePrefix):]); value != "" {
				result.Message = value
			}
			continue
		case strings.HasPrefix(upper, mdzMetricJSONPrefix):
			mergeMDZMetricJSON(trimmed[len(mdzMetricJSONPrefix):], metrics)
			continue
		case strings.HasPrefix(upper, mdzMetricPrefix):
			mergeMDZMetricLine(trimmed[len(mdzMetricPrefix):], metrics)
			continue
		}
		kept = append(kept, line)
	}
	result.Output = strings.TrimSpace(strings.Join(kept, "\n"))

	// Métricas MDZ: último valor vence (map já sobrescreve).
	for key, value := range metrics {
		if !sensorChannelDeclared(declared, key) {
			continue
		}
		putSensorChannel(result.Channels, key, value)
	}

	// Extratores: aplicados na ordem declarada; o último que escrever no canal
	// vence (reflete a ordem do console).
	for _, extractor := range extractors {
		var (
			value float64
			found bool
		)
		switch extractor.Type {
		case "regex":
			value, found = extractRegexChannel(stdout, extractor)
		case "json":
			value, found = extractJSONChannel(stdout, extractor)
		case "mdz":
			value, found = metrics[extractor.Channel]
		}
		if !found {
			continue
		}
		if !sensorChannelDeclared(declared, extractor.Channel) {
			continue
		}
		putSensorChannel(result.Channels, extractor.Channel, value*extractor.Scale)
	}

	return result
}

// sensorChannelDeclared informa se o canal pode ser enviado. Conjunto vazio de
// canais declarados = sensor sem lista (aceita o que o script produzir).
func sensorChannelDeclared(declared map[string]bool, channel string) bool {
	if len(declared) == 0 {
		return true
	}
	if _, ok := declared[strings.ToLower(strings.TrimSpace(channel))]; ok {
		return true
	}
	if _, ok := declared[channel]; ok {
		return true
	}
	return false
}

// putSensorChannel grava o canal descartando NaN/Inf (não são JSON válidos e o
// servidor os rejeitaria no lote inteiro).
func putSensorChannel(channels map[string]float64, key string, value float64) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	// NaN != NaN — única forma portátil de testar sem importar math.
	if value != value || value > 1.7976931348623157e308 || value < -1.7976931348623157e308 {
		return
	}
	channels[key] = value
}

// mergeMDZMetricLine processa "chave=123.4[,outra=2]".
func mergeMDZMetricLine(payload string, metrics map[string]float64) {
	for _, part := range strings.Split(payload, ",") {
		pair := strings.SplitN(part, "=", 2)
		if len(pair) != 2 {
			continue
		}
		key := strings.TrimSpace(pair[0])
		if key == "" {
			continue
		}
		if value, ok := parseLooseFloat(pair[1]); ok {
			metrics[key] = value
		}
	}
}

// mergeMDZMetricJSON processa "MDZ_METRIC_JSON: {"chave": 12.5, ...}".
func mergeMDZMetricJSON(payload string, metrics map[string]float64) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return
	}
	for key, value := range parsed {
		if number, ok := toSensorFloat(value); ok {
			metrics[key] = number
		}
	}
}

// extractRegexChannel roda o regex em todas as linhas e agrega (default: last).
func extractRegexChannel(text string, extractor sensorExtractor) (float64, bool) {
	compiled := extractor.compiled
	if compiled == nil {
		// Extrator montado em código (testes/harness) sem passar por
		// parseSensorExtractors: compila on-demand em vez de descartar.
		if strings.TrimSpace(extractor.Regex) == "" {
			return 0, false
		}
		dynamic, err := regexp.Compile(extractor.Regex)
		if err != nil {
			return 0, false
		}
		compiled = dynamic
	}
	matches := compiled.FindAllStringSubmatch(text, -1)
	values := make([]float64, 0, len(matches))
	for _, match := range matches {
		candidate := ""
		if len(match) > 1 {
			candidate = match[1]
		} else if len(match) > 0 {
			candidate = match[0]
		}
		if value, ok := parseLooseFloat(candidate); ok {
			values = append(values, value)
		}
	}
	return aggregateSensorValues(values, extractor.Aggregate)
}

// extractJSONChannel localiza o primeiro objeto/array JSON do stdout e navega
// no dot path (ex.: "download.bandwidth").
func extractJSONChannel(text string, extractor sensorExtractor) (float64, bool) {
	root, ok := findFirstJSONValue(text)
	if !ok {
		return 0, false
	}
	value, ok := navigateJSONPath(root, extractor.JSONPath)
	if !ok {
		return 0, false
	}
	return value, true
}

// findFirstJSONValue devolve o primeiro valor JSON (objeto ou array) do texto,
// ignorando o que vem antes/depois. Saídas reais (ex.: Ookla) misturam log com
// JSON na mesma saída.
func findFirstJSONValue(text string) (any, bool) {
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '{', '[':
			decoder := json.NewDecoder(strings.NewReader(text[index:]))
			decoder.UseNumber()
			var value any
			if err := decoder.Decode(&value); err == nil {
				return value, true
			}
		}
	}
	return nil, false
}

// navigateJSONPath percorre chaves de objeto e índices de array.
func navigateJSONPath(root any, path string) (float64, bool) {
	current := root
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[part]
			if !ok {
				// Fallback case-insensitive: o JSON do fornecedor pode usar
				// "Bandwidth" enquanto o path declara "bandwidth".
				for key, candidate := range node {
					if strings.EqualFold(key, part) {
						value, ok = candidate, true
						break
					}
				}
			}
			if !ok {
				return 0, false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return 0, false
			}
			current = node[index]
		default:
			return 0, false
		}
	}
	return toSensorFloat(current)
}

// toSensorFloat converte qualquer número/string numérica do JSON em float64.
func toSensorFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case string:
		return parseLooseFloat(typed)
	}
	return 0, false
}

// aggregateSensorValues aplica o agregado declarado (default: last).
func aggregateSensorValues(values []float64, aggregate string) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(aggregate)) {
	case "first":
		return values[0], true
	case "min":
		minimum := values[0]
		for _, value := range values[1:] {
			if value < minimum {
				minimum = value
			}
		}
		return minimum, true
	case "max":
		maximum := values[0]
		for _, value := range values[1:] {
			if value > maximum {
				maximum = value
			}
		}
		return maximum, true
	case "avg", "mean":
		sum := 0.0
		for _, value := range values {
			sum += value
		}
		return sum / float64(len(values)), true
	case "sum":
		sum := 0.0
		for _, value := range values {
			sum += value
		}
		return sum, true
	default: // "last"
		return values[len(values)-1], true
	}
}

// parseLooseFloat aceita "12.5", "12,5" (vírgula decimal é comum em saída de
// ping localizado), "248.7 mbps", "<1ms" e "host:123".
func parseLooseFloat(raw string) (float64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	text = strings.ReplaceAll(text, ",", ".")
	if value, err := strconv.ParseFloat(text, 64); err == nil {
		return value, true
	}
	var builder strings.Builder
	for _, char := range text {
		switch {
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '.' || char == '-' || char == '+':
			builder.WriteRune(char)
		case char == 'e' || char == 'E':
			builder.WriteRune(char)
		}
	}
	candidate := strings.Trim(builder.String(), ".")
	if candidate == "" || candidate == "-" || candidate == "+" {
		return 0, false
	}
	value, err := strconv.ParseFloat(candidate, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
