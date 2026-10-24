{{- define "llm.labels" -}}
app.kubernetes.io/name: llm
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: models-lab
{{- end -}}
{{- define "llm.selectorLabels" -}}
app.kubernetes.io/name: llm
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
