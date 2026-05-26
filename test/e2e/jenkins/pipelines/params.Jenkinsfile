pipeline {
    agent any
    parameters {
        string(name: 'GREETING', defaultValue: 'hello', description: 'How to greet')
        string(name: 'TARGET',   defaultValue: 'world', description: 'Who to greet')
        booleanParam(name: 'LOUD', defaultValue: false, description: 'Use uppercase')
    }
    stages {
        stage('Print') {
            steps {
                script {
                    def msg = "${params.GREETING}, ${params.TARGET}"
                    if (params.LOUD) {
                        msg = msg.toUpperCase()
                    }
                    echo msg
                }
            }
        }
    }
}
