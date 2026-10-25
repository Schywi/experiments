{{- define "sandbox.labels" -}}
app.kubernetes.io/part-of: agent-sandbox-lab
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "sandbox.routerSelector" -}}
app: sandbox-router
app.kubernetes.io/name: sandbox-router
{{- end -}}
