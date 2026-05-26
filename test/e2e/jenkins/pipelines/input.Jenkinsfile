// Spike-input pipeline: used during e2e harness validation (spike 1.2)
// to capture the real Jenkins pendingInputActions API shape.
// This pipeline intentionally pauses at an input step so the harness
// can poll /wfapi/pendingInputActions and record the response.
pipeline {
    agent any
    stages {
        stage('Before') {
            steps {
                echo 'before input'
            }
        }
        stage('Approval') {
            steps {
                input message: 'Proceed?', id: 'spike-input', ok: 'Go'
            }
        }
        stage('After') {
            steps {
                echo 'after input'
            }
        }
    }
}
