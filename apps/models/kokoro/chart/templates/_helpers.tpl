{{- define "kokoro.labels" -}}
app.kubernetes.io/name: kokoro
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: models-lab
{{- end -}}
{{- define "kokoro.selectorLabels" -}}
app.kubernetes.io/name: kokoro
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
