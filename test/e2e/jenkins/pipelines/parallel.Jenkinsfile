pipeline {
    agent any
    stages {
        stage('Build') {
            steps { echo 'building' }
        }
        stage('Test') {
            parallel {
                stage('Unit') {
                    steps { echo 'unit tests' }
                }
                stage('Integration') {
                    steps { echo 'integration tests' }
                }
                stage('Lint') {
                    steps { echo 'lint' }
                }
            }
        }
        stage('Deploy') {
            steps { echo 'deploying' }
        }
    }
}
