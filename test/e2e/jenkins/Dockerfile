# Custom Jenkins image for the jk e2e harness.
#
# We extend the official LTS image so we can:
#   1. Bake in the plugin set up-front (no plugin install on every container
#      start; CI runs become deterministic).
#   2. Disable the setup wizard so JCasC owns first-boot configuration.
#   3. Bake the JCasC YAML + seed Jenkinsfiles into the image so a single
#      `docker compose up -d` is enough to reach a usable state.
#
# Image is built locally (no registry push); the docker-compose service
# references it via `build:`. Pinning the LTS tag — not :latest — so the
# harness stays reproducible across machines and CI runs.
FROM jenkins/jenkins:lts-jdk21

# Skip the post-install setup wizard; JCasC will create the admin user.
# This MUST be set as a JVM system property at startup (Jenkins reads it
# from JAVA_OPTS before bootstrap runs).
ENV JAVA_OPTS="-Djenkins.install.runSetupWizard=false"

# Point JCasC at the bind-mounted YAML directory. The same path is also
# bind-mounted from the host in docker-compose.yml so live edits to
# jcasc/jenkins.yaml are picked up on `docker compose restart`.
ENV CASC_JENKINS_CONFIG=/var/jenkins_home/casc_configs

# Install the plugin set declared in plugins.txt using the official
# Plugin Installation Manager (shipped with the LTS image as
# jenkins-plugin-cli). --plugin-download-directory writes resolved JPI
# files into the Jenkins plugins dir so they are present on first boot.
COPY plugins.txt /usr/share/jenkins/ref/plugins.txt
RUN jenkins-plugin-cli --plugin-file /usr/share/jenkins/ref/plugins.txt

# Bake the JCasC config + seed pipeline scripts into the image. The
# pipelines directory is referenced from jenkins.yaml via readFileFromWorkspace
# so the literal Jenkinsfile contents become inline pipeline definitions on
# the seeded jobs (no SCM checkout required, which keeps the harness fully
# offline).
COPY jcasc/ /var/jenkins_home/casc_configs/
COPY pipelines/ /var/jenkins_home/pipelines/
