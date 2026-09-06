{{- define "worm-controller.labels" -}}
app.kubernetes.io/name: worm-controller
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: bounded-worm-lab
{{- end -}}
{{- define "worm-controller.selectorLabels" -}}
app.kubernetes.io/name: worm-controller
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
