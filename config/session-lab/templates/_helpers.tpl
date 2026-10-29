{{- define "session-lab.labels" -}}
app.kubernetes.io/name: session-lab-autoscaler
app.kubernetes.io/part-of: session-lab
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "session-lab.selector" -}}
app.kubernetes.io/name: session-lab-autoscaler
{{- end -}}
