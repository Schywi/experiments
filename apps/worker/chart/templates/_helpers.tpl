{{- define "worm-worker.labels" -}}
app.kubernetes.io/name: worm-worker
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: bounded-worm-lab
{{- end -}}
{{- define "worm-worker.selectorLabels" -}}
app.kubernetes.io/name: worm-worker
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
