{{- define "assistant.labels" -}}
app.kubernetes.io/name: assistant
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: assistant-lab
{{- end -}}
{{- define "assistant.selectorLabels" -}}
app.kubernetes.io/name: assistant
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
