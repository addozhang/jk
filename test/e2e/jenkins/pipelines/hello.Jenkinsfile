pipeline {
    agent any
    stages {
        stage('Greet') {
            steps {
                echo 'hello from jk e2e harness'
            }
        }
        stage('Archive') {
            steps {
                sh 'mkdir -p dist/nested reports/html'
                writeFile file: 'dist/nested/payload.bin', text: 'AAECA/8=', encoding: 'Base64'
                writeFile file: 'reports/html/index.html', text: '<h1>jk e2e report</h1>'
                archiveArtifacts artifacts: 'dist/**/*.bin,reports/**/*.html'
            }
        }
    }
}
