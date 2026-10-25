{{- define "sandbox-console.labels" -}}
app.kubernetes.io/name: sandbox-console
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: agent-sandbox-lab
{{- end -}}
{{- define "sandbox-console.selectorLabels" -}}
app.kubernetes.io/name: sandbox-console
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
