{{/*
Cut a Kubernetes name or label value to 63 characters and drop trailing
characters that are not alphanumeric. Names and label values must end on
[A-Za-z0-9]; the usual `trunc 63 | trimSuffix "-"` only handles a trailing
"-" and leaves a trailing "." (or "_") behind when the cut lands on one --
the API server then rejects every object that carries the value. Branch
builds trigger this through helm.sh/chart: app-build-suite versions the
chart <semver>-dev.<branch>.<date>.<time>.h<sha>, and for one branch-name
length per dot the 63rd character is a dot (giantswarm/mcp-timescale#7).
sprig's regexReplaceAll takes (regex, input, replacement), so the input is
passed explicitly instead of piped.
*/}}
{{- define "mcp-timescale.trunc63" -}}
{{- regexReplaceAll "[^A-Za-z0-9]+$" (trunc 63 .) "" -}}
{{- end -}}

{{- define "mcp-timescale.name" -}}
{{- include "mcp-timescale.trunc63" (default .Chart.Name .Values.nameOverride) -}}
{{- end -}}

{{- define "mcp-timescale.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- include "mcp-timescale.trunc63" .Values.fullnameOverride -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- include "mcp-timescale.trunc63" .Release.Name -}}
{{- else -}}
{{- include "mcp-timescale.trunc63" (printf "%s-%s" .Release.Name $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "mcp-timescale.labels" -}}
app.kubernetes.io/name: {{ include "mcp-timescale.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
application.giantswarm.io/team: {{ index .Chart.Annotations "io.giantswarm.application.team" | quote }}
helm.sh/chart: {{ include "mcp-timescale.trunc63" (printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_") }}
{{- end -}}

{{- define "mcp-timescale.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mcp-timescale.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "mcp-timescale.serviceAccountName" -}}
{{- $sa := (default dict .Values.serviceAccount) -}}
{{- if $sa.create -}}
{{- default (include "mcp-timescale.fullname" .) $sa.name -}}
{{- else -}}
{{- default "default" $sa.name -}}
{{- end -}}
{{- end -}}

{{- define "mcp-timescale.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
